package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	baselineagent "zoa/baselineagent"
)

const defaultChatSystemPrompt = `You are an assistant in a persistent chat session.
Use tools when they help. Be concise and factual.`

type ServiceConfig struct {
	SessionID    string
	SessionDir   string
	TaskLogDir   string
	APIKey       string
	CWD          string
	Model        string
	MaxTurns     int
	Timeout      time.Duration
	Temperature  float64
	SystemPrompt string
	Tools        []baselineagent.Tool
}

type Service struct {
	mu sync.Mutex

	cfg           ServiceConfig
	store         *Store
	snapshot      Snapshot
	conversation  baselineagent.Conversation
	workerRunning bool
}

func NewService(cfg ServiceConfig) (*Service, error) {
	cwd := strings.TrimSpace(cfg.CWD)
	if cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("resolve cwd: %w", err)
		}
		cwd = wd
	}
	absCWD, err := filepath.Abs(cwd)
	if err != nil {
		return nil, fmt.Errorf("resolve absolute cwd: %w", err)
	}
	cfg.CWD = absCWD

	storeDir := strings.TrimSpace(cfg.SessionDir)
	sessionID := strings.TrimSpace(cfg.SessionID)
	if storeDir == "" {
		if sessionID == "" {
			sessionID = "default"
		}
		storeDir = filepath.Join(".gateway", "sessions", sessionID)
	} else if sessionID == "" {
		sessionID = defaultSessionIDFromStoreDir(storeDir)
	}
	cfg.SessionID = sessionID
	taskLogDir := strings.TrimSpace(cfg.TaskLogDir)
	if taskLogDir == "" {
		taskLogDir = filepath.Join(storeDir, "tasks")
	}
	cfg.TaskLogDir = taskLogDir
	store, err := NewStore(storeDir)
	if err != nil {
		return nil, err
	}

	snapshot, err := store.LoadSnapshot(sessionID)
	if err != nil {
		return nil, err
	}

	svc := &Service{
		cfg:      cfg,
		store:    store,
		snapshot: snapshot,
	}
	svc.Start()
	return svc, nil
}

func defaultSessionIDFromStoreDir(storeDir string) string {
	base := filepath.Base(filepath.Clean(strings.TrimSpace(storeDir)))
	if base == "" || base == "." || base == string(filepath.Separator) {
		return "default"
	}
	return base
}

func (s *Service) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.workerRunning || len(s.snapshot.Queue) == 0 {
		return
	}
	s.workerRunning = true
	go s.workerLoop()
}

func (s *Service) Receive(channel, text string) (ReceiveResult, error) {
	channel = strings.TrimSpace(channel)
	if channel == "" {
		channel = "tui"
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return ReceiveResult{}, fmt.Errorf("message cannot be empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if strings.HasPrefix(text, "/") {
		return s.handleSlashLocked(channel, text)
	}

	now := time.Now().UTC()
	s.snapshot.NextInboundID++
	msg := InboundMessage{
		ID:         s.snapshot.NextInboundID,
		Channel:    channel,
		Text:       text,
		ReceivedAt: now,
	}
	s.snapshot.Queue = append(s.snapshot.Queue, msg)

	decision := "queued"
	if !s.workerRunning {
		s.workerRunning = true
		decision = "queued_and_started"
		go s.workerLoop()
	}
	if err := s.saveLocked(); err != nil {
		return ReceiveResult{}, err
	}
	return ReceiveResult{
		Accepted:  true,
		MessageID: msg.ID,
		Decision:  decision,
		QueueLen:  len(s.snapshot.Queue),
	}, nil
}

func (s *Service) OutboxSince(lastID int64) []OutboundMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []OutboundMessage{}
	for _, msg := range s.snapshot.Outbox {
		if msg.ID > lastID {
			out = append(out, msg)
		}
	}
	return out
}

func (s *Service) StatusSnapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneSnapshot(s.snapshot)
}

func (s *Service) workerLoop() {
	for {
		msg, ok := s.dequeueForRun()
		if !ok {
			return
		}

		reply, runErr := s.processMessage(msg)
		if runErr != nil {
			reply = fmt.Sprintf("Failed to process message %d: %v", msg.ID, runErr)
		}

		s.mu.Lock()
		_, _ = s.sendLocked(msg.Channel, reply, msg.ID)
		s.snapshot.Active = nil
		if s.conversation != nil {
			s.snapshot.Conversation = s.conversation.History()
		}
		if len(s.snapshot.Queue) == 0 {
			s.workerRunning = false
		}
		_ = s.saveLocked()
		shouldContinue := s.workerRunning
		s.mu.Unlock()

		if !shouldContinue {
			return
		}
	}
}

func (s *Service) dequeueForRun() (InboundMessage, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.snapshot.Queue) == 0 {
		s.workerRunning = false
		_ = s.saveLocked()
		return InboundMessage{}, false
	}
	msg := s.snapshot.Queue[0]
	s.snapshot.Queue = s.snapshot.Queue[1:]
	s.snapshot.Active = &ActiveRun{
		MessageID: msg.ID,
		Channel:   msg.Channel,
		StartedAt: time.Now().UTC(),
	}
	_ = s.saveLocked()
	return msg, true
}

func (s *Service) processMessage(msg InboundMessage) (string, error) {
	conversation, err := s.ensureConversation()
	if err != nil {
		return "", err
	}
	res, err := conversation.Prompt(context.Background(), msg.Text)
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(res.FinalResponse)
	if text == "" {
		text = "(no response)"
	}
	return text, nil
}

func (s *Service) ensureConversation() (baselineagent.Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conversation != nil {
		return s.conversation, nil
	}

	tools := s.cfg.Tools
	if len(tools) == 0 {
		builtins, err := baselineagent.NewBuiltinCodingTools(s.cfg.CWD)
		if err != nil {
			return nil, fmt.Errorf("initialize builtin tools: %w", err)
		}
		tools = builtins
	}
	systemPrompt := strings.TrimSpace(s.cfg.SystemPrompt)
	if systemPrompt == "" {
		systemPrompt = defaultChatSystemPrompt
	}

	conv, err := baselineagent.NewConversation(s.cfg.APIKey, baselineagent.ConversationConfig{
		CWD:             s.cfg.CWD,
		Model:           s.cfg.Model,
		MaxTurns:        s.cfg.MaxTurns,
		Timeout:         s.cfg.Timeout,
		Temperature:     s.cfg.Temperature,
		SystemPrompt:    systemPrompt,
		Tools:           tools,
		InitialMessages: s.snapshot.Conversation,
	})
	if err != nil {
		return nil, err
	}
	s.conversation = conv
	return s.conversation, nil
}

func (s *Service) handleSlashLocked(channel, text string) (ReceiveResult, error) {
	command := strings.Fields(strings.TrimSpace(text))
	if len(command) == 0 {
		return ReceiveResult{}, fmt.Errorf("invalid command")
	}

	reply, err := s.renderSlashResponseLocked(command[0])
	if err != nil {
		return ReceiveResult{}, err
	}

	out, err := s.sendLocked(channel, reply, 0)
	if err != nil {
		return ReceiveResult{}, err
	}
	if err := s.saveLocked(); err != nil {
		return ReceiveResult{}, err
	}

	return ReceiveResult{
		Accepted:  true,
		MessageID: out.ID,
		Decision:  "slash_handled",
		QueueLen:  len(s.snapshot.Queue),
	}, nil
}

func (s *Service) renderSlashResponseLocked(command string) (string, error) {
	switch command {
	case "/status":
		state := "idle"
		active := "none"
		if s.snapshot.Active != nil {
			state = "running"
			active = fmt.Sprintf("message %d (%s)", s.snapshot.Active.MessageID, s.snapshot.Active.Channel)
		}
		return fmt.Sprintf(
			"Session: %s\nState: %s\nActive: %s\nQueue: %d\nOutbox: %d",
			s.snapshot.SessionID,
			state,
			active,
			len(s.snapshot.Queue),
			len(s.snapshot.Outbox),
		), nil
	case "/queue":
		if len(s.snapshot.Queue) == 0 {
			return "Queue is empty.", nil
		}
		lines := []string{"Queued messages:"}
		for _, m := range s.snapshot.Queue {
			lines = append(lines, fmt.Sprintf("- #%d [%s] %s", m.ID, m.Channel, preview(m.Text, 80)))
		}
		return strings.Join(lines, "\n"), nil
	case "/outbox":
		if len(s.snapshot.Outbox) == 0 {
			return "Outbox is empty.", nil
		}
		start := 0
		if len(s.snapshot.Outbox) > 10 {
			start = len(s.snapshot.Outbox) - 10
		}
		lines := []string{"Recent outbox messages:"}
		for _, m := range s.snapshot.Outbox[start:] {
			lines = append(lines, fmt.Sprintf("- #%d [%s] %s", m.ID, m.Channel, preview(m.Text, 80)))
		}
		return strings.Join(lines, "\n"), nil
	case "/log":
		items, err := s.readTaskLogSummariesLocked(20, false)
		if err != nil {
			return "", err
		}
		if len(items) == 0 {
			return "No task logs yet.", nil
		}
		lines := []string{"Recent tasks:"}
		for _, line := range items {
			lines = append(lines, "- "+line)
		}
		return strings.Join(lines, "\n"), nil
	case "/tasks":
		items, err := s.readTaskLogSummariesLocked(0, true)
		if err != nil {
			return "", err
		}
		if len(items) == 0 {
			return "No running tasks.", nil
		}
		lines := []string{"Running tasks:"}
		for _, line := range items {
			lines = append(lines, "- "+line)
		}
		return strings.Join(lines, "\n"), nil
	default:
		return "Unknown slash command. Available: /status /queue /log /tasks /outbox", nil
	}
}

func (s *Service) sendLocked(channel, text string, inReplyTo int64) (OutboundMessage, error) {
	channel = strings.TrimSpace(channel)
	if channel == "" {
		channel = "tui"
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return OutboundMessage{}, fmt.Errorf("outbound text cannot be empty")
	}

	s.snapshot.NextOutboundID++
	out := OutboundMessage{
		ID:        s.snapshot.NextOutboundID,
		Channel:   channel,
		Text:      text,
		InReplyTo: inReplyTo,
		SentAt:    time.Now().UTC(),
	}
	s.snapshot.Outbox = append(s.snapshot.Outbox, out)
	if len(s.snapshot.Outbox) > 500 {
		s.snapshot.Outbox = s.snapshot.Outbox[len(s.snapshot.Outbox)-500:]
	}
	return out, nil
}

func (s *Service) saveLocked() error {
	s.snapshot.UpdatedAt = time.Now().UTC()
	return s.store.SaveSnapshot(cloneSnapshot(s.snapshot))
}

func (s *Service) readTaskLogSummariesLocked(limit int, onlyRunning bool) ([]string, error) {
	taskDir := strings.TrimSpace(s.cfg.TaskLogDir)
	if taskDir == "" {
		return []string{}, nil
	}
	entries, err := os.ReadDir(taskDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("read task log dir: %w", err)
	}
	files := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, "task-") && strings.HasSuffix(name, ".json") {
			files = append(files, name)
		}
	}
	sort.Strings(files)
	if limit > 0 && len(files) > limit {
		files = files[len(files)-limit:]
	}
	// reverse for newest first (lexicographically highest task id first)
	for i, j := 0, len(files)-1; i < j; i, j = i+1, j-1 {
		files[i], files[j] = files[j], files[i]
	}

	lines := make([]string, 0, len(files))
	for _, name := range files {
		path := filepath.Join(taskDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var parsed struct {
			TaskID     string `json:"task_id"`
			FunctionID string `json:"function_id"`
			Status     string `json:"status"`
			Error      string `json:"error,omitempty"`
			UpdatedAt  string `json:"updated_at,omitempty"`
		}
		if err := json.Unmarshal(data, &parsed); err != nil {
			continue
		}
		if onlyRunning && parsed.Status != "running" {
			continue
		}
		line := fmt.Sprintf("%s %s [%s]", parsed.TaskID, parsed.FunctionID, parsed.Status)
		if parsed.Error != "" {
			line += " error=" + preview(parsed.Error, 80)
		}
		if parsed.UpdatedAt != "" {
			line += " updated_at=" + parsed.UpdatedAt
		}
		lines = append(lines, line)
	}
	return lines, nil
}

func cloneSnapshot(in Snapshot) Snapshot {
	out := in
	out.Queue = append([]InboundMessage(nil), in.Queue...)
	if out.Queue == nil {
		out.Queue = []InboundMessage{}
	}
	out.Outbox = append([]OutboundMessage(nil), in.Outbox...)
	if out.Outbox == nil {
		out.Outbox = []OutboundMessage{}
	}
	if in.Conversation != nil {
		data, err := json.Marshal(in.Conversation)
		if err == nil {
			var conv []baselineagent.ConversationMessage
			if err := json.Unmarshal(data, &conv); err == nil {
				out.Conversation = conv
			}
		}
	} else {
		out.Conversation = []baselineagent.ConversationMessage{}
	}
	if in.Active != nil {
		active := *in.Active
		out.Active = &active
	}
	return out
}

func preview(text string, max int) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\n", " "))
	if len(text) <= max {
		return text
	}
	if max <= 3 {
		return text[:max]
	}
	return text[:max-3] + "..."
}
