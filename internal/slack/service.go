package slack

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"strings"
	"sync"
	"time"

	"zoa/internal/gatewaychannel"
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
	Logger             *slog.Logger
	RawClient          RawClient
}

type Service struct {
	cfg     Config
	gateway gatewayclient.GatewayClient
	slack   RawClient
	logger  *slog.Logger
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

	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	logger := cfg.Logger.With("component", "slack")

	if cfg.RawClient == nil {
		rawClient, err := NewRawClient(RawClientConfig{
			AppToken: cfg.AppToken,
			BotToken: cfg.BotToken,
			Logger:   logger,
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

	return &Service{
		cfg:     cfg,
		gateway: gateway,
		slack:   cfg.RawClient,
		logger:  logger,
		seen:    map[string]time.Time{},
	}, nil
}

func (s *Service) Run(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	lastOutboxID, err := s.gateway.OutboxMaxID()
	if err != nil {
		return fmt.Errorf("read outbox position: %w", err)
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
				s.logger.Error("event handling error", "error", err)
			}
			return nil
		})
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			s.logger.Warn("socket session ended", "error", err)
		}

		sleep := withJitter(backoff, 0.25)
		s.logger.Info("reconnecting", "delay", sleep)
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

	result, err := s.gateway.Enqueue(text, gatewaychannel.SlackURI(event.ChannelID))
	if err != nil {
		return err
	}
	s.logger.Info("message enqueued",
		"inbound_id", result.InboundID,
		"decision", result.Decision,
		"queue_len", result.QueueLen,
	)
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
		s.logger.Info("bound to DM channel", "channel", channelID)
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
			messages, _, err := s.gateway.OutboxSince(lastID, s.cfg.OutboxLimit)
			if err != nil {
				s.logger.Error("outbox poll error", "error", err)
				continue
			}

			for _, msg := range messages {
				channelID, ok := s.resolveSlackOutboxChannel(msg)
				if !ok {
					lastID = msg.ID
					continue
				}
				if err := s.sendWithRetry(ctx, channelID, msg.Text); err != nil {
					s.logger.Error("outbox delivery error", "outbox_id", msg.ID, "channel", channelID, "error", err)
					break
				}
				s.logger.Info("outbox delivered", "outbox_id", msg.ID, "channel", channelID)
				lastID = msg.ID
			}
		}
	}
}

func (s *Service) resolveSlackOutboxChannel(msg gatewayclient.OutboxMessage) (string, bool) {
	channelURI := strings.TrimSpace(msg.Channel)
	if channelURI == "" {
		s.logger.Warn("outbox message missing channel URI", "outbox_id", msg.ID)
		return "", false
	}
	target, err := gatewaychannel.Parse(channelURI)
	if err != nil {
		s.logger.Error("invalid outbox channel URI", "outbox_id", msg.ID, "channel", msg.Channel, "error", err)
		return "", false
	}
	switch target.Transport {
	case gatewaychannel.TransportSlack:
		channelID := strings.TrimSpace(target.SlackChannelID)
		if channelID != "" {
			return channelID, true
		}
		s.logger.Error("slack outbox missing channel_id", "outbox_id", msg.ID, "channel", msg.Channel)
		return "", false
	case gatewaychannel.TransportTUI:
		return "", false
	default:
		s.logger.Warn("outbox transport not handled by slack bridge", "outbox_id", msg.ID, "transport", target.Transport)
		return "", false
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
