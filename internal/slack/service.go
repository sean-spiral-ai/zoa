package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"zoa/internal/gatewayclient"
)

const (
	defaultOutboxPollInterval = 400 * time.Millisecond
	defaultOutboxLimit        = 100
	initialReconnectDelay     = 2 * time.Second
	maxReconnectDelay         = 30 * time.Second
)

type Config struct {
	AppToken           string
	BotToken           string
	OutboxPollInterval time.Duration
	OutboxLimit        int
	CursorPath         string
	Logger             *log.Logger
	RawClient          RawClient
}

type Service struct {
	cfg     Config
	gateway gatewayclient.GatewayClient
	slack   RawClient
	logger  *log.Logger
	mu      sync.Mutex

	boundDMID string
	seen      map[string]time.Time
}

func NewService(cfg Config, gateway gatewayclient.GatewayClient) (*Service, error) {
	if gateway == nil {
		return nil, fmt.Errorf("gateway client is nil")
	}
	cfg.AppToken = strings.TrimSpace(cfg.AppToken)
	cfg.BotToken = strings.TrimSpace(cfg.BotToken)
	if cfg.RawClient == nil {
		rawClient, err := NewRawClient(RawClientConfig{
			AppToken: cfg.AppToken,
			BotToken: cfg.BotToken,
			Logger:   cfg.Logger,
		})
		if err != nil {
			return nil, err
		}
		cfg.RawClient = rawClient
	}

	if cfg.OutboxPollInterval <= 0 {
		cfg.OutboxPollInterval = defaultOutboxPollInterval
	}
	if cfg.OutboxLimit <= 0 {
		cfg.OutboxLimit = defaultOutboxLimit
	}
	if cfg.Logger == nil {
		cfg.Logger = log.New(os.Stderr, "slack: ", log.LstdFlags)
	}

	return &Service{
		cfg:     cfg,
		gateway: gateway,
		slack:   cfg.RawClient,
		logger:  cfg.Logger,
		seen:    map[string]time.Time{},
	}, nil
}

func (s *Service) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	lastOutboxID, err := loadCursor(s.cfg.CursorPath)
	if err != nil {
		return err
	}

	outboxCtx, outboxCancel := context.WithCancel(ctx)
	defer outboxCancel()
	go s.runOutboxLoop(outboxCtx, lastOutboxID)

	backoff := initialReconnectDelay
	for {
		if ctx.Err() != nil {
			return nil
		}
		err := s.slack.RunSocketMode(ctx, func(cbCtx context.Context, event DMEvent) error {
			if err := s.handleDMEvent(event); err != nil {
				s.logger.Printf("event handling error: %v", err)
			}
			return nil
		})
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			s.logger.Printf("socket session ended: %v", err)
		}

		sleep := withJitter(backoff, 0.25)
		timer := time.NewTimer(sleep)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		backoff = time.Duration(math.Min(float64(backoff)*1.8, float64(maxReconnectDelay)))
	}
}

func (s *Service) handleDMEvent(event DMEvent) error {
	if strings.TrimSpace(event.ChannelID) == "" || strings.TrimSpace(event.UserID) == "" {
		return nil
	}
	text := strings.TrimSpace(event.Text)
	if text == "" {
		return nil
	}

	dedupeKey := strings.TrimSpace(event.EventID)
	if dedupeKey == "" {
		dedupeKey = strings.TrimSpace(event.ChannelID) + ":" + strings.TrimSpace(event.TS)
	}
	if s.isDuplicate(dedupeKey) {
		return nil
	}

	if !s.bindChannel(event.ChannelID) {
		_ = s.slack.PostMessage(context.Background(), event.ChannelID, "This zoa bridge is currently bound to a different DM channel.")
		return nil
	}

	if _, err := s.gateway.Enqueue(text); err != nil {
		return err
	}
	return nil
}

func (s *Service) bindChannel(channelID string) bool {
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.boundDMID == "" {
		s.boundDMID = channelID
		s.logger.Printf("bound to DM channel %s", channelID)
		return true
	}
	return s.boundDMID == channelID
}

func (s *Service) boundChannel() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.boundDMID
}

func (s *Service) isDuplicate(key string) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	now := time.Now().UTC()
	expiryCutoff := now.Add(-24 * time.Hour)

	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.seen[key]; ok {
		if t.After(expiryCutoff) {
			return true
		}
	}
	s.seen[key] = now
	for k, t := range s.seen {
		if t.Before(expiryCutoff) {
			delete(s.seen, k)
		}
	}
	return false
}

func (s *Service) runOutboxLoop(ctx context.Context, initialLastID int64) {
	lastID := initialLastID
	ticker := time.NewTicker(s.cfg.OutboxPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			boundChannel := s.boundChannel()
			if strings.TrimSpace(boundChannel) == "" {
				continue
			}

			messages, _, err := s.gateway.OutboxSince(lastID, s.cfg.OutboxLimit)
			if err != nil {
				s.logger.Printf("outbox poll error: %v", err)
				continue
			}

			deliveredID := lastID
			for _, msg := range messages {
				if err := s.sendWithRetry(ctx, boundChannel, msg.Text); err != nil {
					s.logger.Printf("outbox delivery error (id=%d): %v", msg.ID, err)
					break
				}
				deliveredID = msg.ID
			}
			if deliveredID > lastID {
				lastID = deliveredID
				if err := saveCursor(s.cfg.CursorPath, lastID); err != nil {
					s.logger.Printf("cursor save error: %v", err)
				}
			}
		}
	}
}

func (s *Service) sendWithRetry(ctx context.Context, channelID, text string) error {
	err := s.slack.PostMessage(ctx, channelID, text)
	if err == nil {
		return nil
	}
	var rateErr *rateLimitError
	if !errors.As(err, &rateErr) {
		return err
	}
	wait := rateErr.retryAfter
	if wait <= 0 {
		wait = time.Second
	}
	timer := time.NewTimer(wait)
	select {
	case <-ctx.Done():
		timer.Stop()
		return ctx.Err()
	case <-timer.C:
	}
	return s.slack.PostMessage(ctx, channelID, text)
}

type cursorState struct {
	LastID int64 `json:"last_id"`
}

func loadCursor(path string) (int64, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return 0, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read cursor file: %w", err)
	}
	var state cursorState
	if err := json.Unmarshal(data, &state); err != nil {
		return 0, fmt.Errorf("decode cursor file: %w", err)
	}
	if state.LastID < 0 {
		state.LastID = 0
	}
	return state.LastID, nil
}

func saveCursor(path string, lastID int64) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir cursor dir: %w", err)
	}
	data, err := json.Marshal(cursorState{LastID: lastID})
	if err != nil {
		return err
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("write cursor temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename cursor file: %w", err)
	}
	return nil
}

func withJitter(base time.Duration, ratio float64) time.Duration {
	if base <= 0 {
		return 0
	}
	if ratio <= 0 {
		return base
	}
	maxJitter := int64(float64(base) * ratio)
	if maxJitter <= 0 {
		return base
	}
	jitter := time.Duration(rand.Int63n(maxJitter))
	return base + jitter
}
