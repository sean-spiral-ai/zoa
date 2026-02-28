package baselineagent

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"codexagentbase/baselineagent/internal/agent"
	"codexagentbase/baselineagent/internal/llm"
	"codexagentbase/baselineagent/internal/tools"
)

type ConversationConfig struct {
	CWD          string
	Model        string
	MaxTurns     int
	Timeout      time.Duration
	Temperature  float64
	VerboseLog   io.Writer
	SystemPrompt string
}

type Conversation interface {
	Prompt(ctx context.Context, instruction string) (RunResult, error)
	PromptStructured(ctx context.Context, instruction string, format StructuredResponseFormat) (RunResult, error)
	Fork() Conversation
}

type defaultConversation struct {
	session *agent.Session
	timeout time.Duration
}

func NewConversation(apiKey string, cfg ConversationConfig) (Conversation, error) {
	session, timeout, err := newAgentSession(apiKey, cfg)
	if err != nil {
		return nil, err
	}
	return &defaultConversation{session: session, timeout: timeout}, nil
}

func (c *defaultConversation) Prompt(ctx context.Context, instruction string) (RunResult, error) {
	return c.promptWithOptions(ctx, instruction, agent.PromptOptions{})
}

func (c *defaultConversation) PromptStructured(
	ctx context.Context,
	instruction string,
	format StructuredResponseFormat,
) (RunResult, error) {
	if format == nil {
		return RunResult{}, fmt.Errorf("structured response format cannot be nil")
	}
	return c.promptWithOptions(ctx, instruction, agent.PromptOptions{
		ResponseMimeType: format.MimeType(),
		ResponseSchema:   format.Schema(),
		DisableTools:     true,
	})
}

func (c *defaultConversation) promptWithOptions(ctx context.Context, instruction string, options agent.PromptOptions) (RunResult, error) {
	runCtx := ctx
	cancel := func() {}
	if c.timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, c.timeout)
	}
	defer cancel()

	sessionResult, err := c.session.PromptWithOptions(runCtx, instruction, options)
	out := RunResult{
		FinalResponse: sessionResult.FinalResponse,
		Turns:         sessionResult.Turns,
		ToolCalls:     countToolCalls(sessionResult.Messages),
		MessageCount:  len(sessionResult.Messages),
	}
	if err != nil {
		return out, err
	}
	return out, nil
}

func (c *defaultConversation) Fork() Conversation {
	return &defaultConversation{
		session: c.session.Fork(),
		timeout: c.timeout,
	}
}

func newAgentSession(apiKey string, cfg ConversationConfig) (*agent.Session, time.Duration, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, 0, fmt.Errorf("api key is required")
	}
	if strings.TrimSpace(cfg.CWD) == "" {
		return nil, 0, fmt.Errorf("cwd is required")
	}

	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = DefaultModel
	}
	maxTurns := cfg.MaxTurns
	if maxTurns <= 0 {
		maxTurns = DefaultMaxTurns
	}
	temperature := cfg.Temperature
	if temperature == 0 {
		temperature = DefaultTemperature
	}

	toolset, err := tools.NewCodingTools(cfg.CWD)
	if err != nil {
		return nil, 0, fmt.Errorf("initialize tools: %w", err)
	}

	systemPrompt := strings.TrimSpace(cfg.SystemPrompt)
	if systemPrompt == "" {
		systemPrompt = agent.DefaultSystemPrompt
	}

	client := llm.NewGeminiClient(apiKey)
	session, err := agent.NewSession(agent.SessionConfig{
		Client:       client,
		Model:        model,
		Tools:        toolset,
		Temperature:  temperature,
		MaxTurns:     maxTurns,
		SystemPrompt: systemPrompt,
		VerboseLog:   cfg.VerboseLog,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("create session: %w", err)
	}

	return session, cfg.Timeout, nil
}
