package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
)

type DMEvent struct {
	EventID   string
	ChannelID string
	UserID    string
	Text      string
	TS        string
}

type RawClient interface {
	RunSocketMode(ctx context.Context, onDM func(context.Context, DMEvent) error) error
	PostMessage(ctx context.Context, channelID, text string) error
}

type RawClientConfig struct {
	AppToken   string
	BotToken   string
	HTTPClient *http.Client
	Logger     *log.Logger
}

type rawClient struct {
	appToken string
	botToken string
	http     *http.Client
	logger   *log.Logger
}

type socketEnvelope struct {
	EnvelopeID string          `json:"envelope_id"`
	Type       string          `json:"type"`
	Payload    json.RawMessage `json:"payload"`
}

type socketOpenResponse struct {
	OK    bool   `json:"ok"`
	URL   string `json:"url"`
	Error string `json:"error"`
}

type eventsAPIPayload struct {
	EventID string    `json:"event_id"`
	Event   dmMessage `json:"event"`
}

type dmMessage struct {
	Type        string `json:"type"`
	Subtype     string `json:"subtype"`
	BotID       string `json:"bot_id"`
	ChannelType string `json:"channel_type"`
	Channel     string `json:"channel"`
	User        string `json:"user"`
	Text        string `json:"text"`
	TS          string `json:"ts"`
}

type chatPostMessageResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
	TS    string `json:"ts"`
}

type rateLimitError struct {
	retryAfter time.Duration
}

func (e *rateLimitError) Error() string {
	return fmt.Sprintf("slack rate limited, retry after %s", e.retryAfter)
}

func NewRawClient(cfg RawClientConfig) (RawClient, error) {
	appToken := strings.TrimSpace(cfg.AppToken)
	botToken := strings.TrimSpace(cfg.BotToken)
	if appToken == "" {
		return nil, fmt.Errorf("slack app token is required")
	}
	if botToken == "" {
		return nil, fmt.Errorf("slack bot token is required")
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}

	return &rawClient{
		appToken: appToken,
		botToken: botToken,
		http:     httpClient,
		logger:   cfg.Logger,
	}, nil
}

func (c *rawClient) RunSocketMode(ctx context.Context, onDM func(context.Context, DMEvent) error) error {
	if onDM == nil {
		return fmt.Errorf("onDM handler is required")
	}

	wsURL, err := c.openSocketURL(ctx)
	if err != nil {
		return err
	}
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "bye") }()

	for {
		_, payload, err := conn.Read(ctx)
		if err != nil {
			return err
		}

		var envelope socketEnvelope
		if err := json.Unmarshal(payload, &envelope); err != nil {
			continue
		}
		if envelope.Type == "hello" {
			if c.logger != nil {
				c.logger.Printf("socket connected")
			}
			continue
		}
		if strings.TrimSpace(envelope.EnvelopeID) != "" {
			if err := c.ackEnvelope(ctx, conn, envelope.EnvelopeID); err != nil {
				return err
			}
		}
		if envelope.Type != "events_api" {
			continue
		}

		event, ok, err := decodeDMEvent(envelope.Payload)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if err := onDM(ctx, event); err != nil {
			return err
		}
	}
}

func (c *rawClient) PostMessage(ctx context.Context, channelID, text string) error {
	body, err := json.Marshal(map[string]string{
		"channel": channelID,
		"text":    text,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://slack.com/api/chat.postMessage", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.botToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return &rateLimitError{retryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
	}

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var parsed chatPostMessageResponse
	if err := json.Unmarshal(responseBody, &parsed); err != nil {
		return fmt.Errorf("decode chat.postMessage response: %w", err)
	}
	if !parsed.OK {
		errText := strings.TrimSpace(parsed.Error)
		if errText == "" {
			errText = "unknown"
		}
		return fmt.Errorf("chat.postMessage: %s", errText)
	}
	return nil
}

func decodeDMEvent(payload json.RawMessage) (DMEvent, bool, error) {
	var body eventsAPIPayload
	if err := json.Unmarshal(payload, &body); err != nil {
		return DMEvent{}, false, err
	}
	event := body.Event
	if event.Type != "message" || event.ChannelType != "im" {
		return DMEvent{}, false, nil
	}
	if strings.TrimSpace(event.Subtype) != "" {
		return DMEvent{}, false, nil
	}
	if strings.TrimSpace(event.BotID) != "" {
		return DMEvent{}, false, nil
	}
	if strings.TrimSpace(event.Channel) == "" || strings.TrimSpace(event.User) == "" {
		return DMEvent{}, false, nil
	}
	text := strings.TrimSpace(event.Text)
	if text == "" {
		return DMEvent{}, false, nil
	}
	return DMEvent{
		EventID:   strings.TrimSpace(body.EventID),
		ChannelID: strings.TrimSpace(event.Channel),
		UserID:    strings.TrimSpace(event.User),
		Text:      text,
		TS:        strings.TrimSpace(event.TS),
	}, true, nil
}

func (c *rawClient) ackEnvelope(ctx context.Context, conn *websocket.Conn, envelopeID string) error {
	ackPayload, err := json.Marshal(map[string]string{"envelope_id": envelopeID})
	if err != nil {
		return err
	}
	ackCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return conn.Write(ackCtx, websocket.MessageText, ackPayload)
}

func (c *rawClient) openSocketURL(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://slack.com/api/apps.connections.open", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.appToken)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	var parsed socketOpenResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("decode socket open response: %w", err)
	}
	if !parsed.OK {
		errText := strings.TrimSpace(parsed.Error)
		if errText == "" {
			errText = "unknown"
		}
		return "", fmt.Errorf("apps.connections.open: %s", errText)
	}
	if strings.TrimSpace(parsed.URL) == "" {
		return "", fmt.Errorf("apps.connections.open returned empty websocket url")
	}
	return parsed.URL, nil
}

func parseRetryAfter(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}
