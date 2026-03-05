package baselineagent

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"zoa/baselineagent/internal/agent"
	"zoa/baselineagent/internal/llm"
	"zoa/internal/llmtrace"
)

type ConversationConfig struct {
	CWD             string
	Model           string
	MaxTurns        int
	Timeout         time.Duration
	Temperature     float64
	VerboseLog      io.Writer
	SystemPrompt    string
	Tools           []Tool
	InitialMessages []ConversationMessage
	Tracer          llmtrace.MessageTracer
}

type Conversation interface {
	Prompt(ctx context.Context, instruction string) (RunResult, error)
	PromptStructured(ctx context.Context, instruction string, format StructuredResponseFormat) (RunResult, error)
	AppendMessages(messages []ConversationMessage) error
	Fork() Conversation
	History() []ConversationMessage
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

func (c *defaultConversation) AppendMessages(messages []ConversationMessage) error {
	if len(messages) == 0 {
		return nil
	}
	c.session.AppendMessages(toLLMMessages(messages))
	return nil
}

func (c *defaultConversation) History() []ConversationMessage {
	return fromLLMMessages(c.session.Messages())
}

func newAgentSession(apiKey string, cfg ConversationConfig) (*agent.Session, time.Duration, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, 0, fmt.Errorf("credential is required")
	}
	if strings.TrimSpace(cfg.CWD) == "" {
		return nil, 0, fmt.Errorf("cwd is required")
	}

	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = DefaultModel
	}
	if !IsSupportedModel(model) {
		return nil, 0, fmt.Errorf("unsupported model %q (supported: %s)", model, strings.Join(SupportedModelNames(), ", "))
	}
	provider := InferProviderFromModel(model)
	if !provider.Valid() {
		return nil, 0, fmt.Errorf("unsupported model %q (supported: %s)", model, strings.Join(SupportedModelNames(), ", "))
	}
	maxTurns := cfg.MaxTurns
	if maxTurns <= 0 {
		maxTurns = DefaultMaxTurns
	}
	temperature := cfg.Temperature
	if temperature == 0 {
		temperature = DefaultTemperature
	}

	if len(cfg.Tools) == 0 {
		return nil, 0, fmt.Errorf("at least one tool is required")
	}
	toolset := cfg.Tools

	systemPrompt := strings.TrimSpace(cfg.SystemPrompt)
	if systemPrompt == "" {
		systemPrompt = agent.DefaultSystemPrompt
	}

	client, err := newLLMClient(provider, apiKey)
	if err != nil {
		return nil, 0, err
	}
	session, err := agent.NewSession(agent.SessionConfig{
		Client:          client,
		Model:           model,
		Tools:           toolset,
		Temperature:     temperature,
		MaxTurns:        maxTurns,
		SystemPrompt:    systemPrompt,
		VerboseLog:      cfg.VerboseLog,
		InitialMessages: toLLMMessages(cfg.InitialMessages),
		Tracer:          cfg.Tracer,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("create session: %w", err)
	}

	return session, cfg.Timeout, nil
}

func newLLMClient(provider Provider, credential string) (llm.Client, error) {
	switch provider {
	case ProviderGemini:
		return llm.NewGeminiClient(credential), nil
	case ProviderAnthropic:
		return llm.NewAnthropicClientWithOAuthToken(credential), nil
	default:
		return nil, fmt.Errorf("unsupported provider %q", provider)
	}
}
