package agent

import (
	"context"
	"fmt"
	"io"
	"strings"

	builtintools "zoa/baselineagent/builtintools"
	"zoa/baselineagent/internal/llm"
)

type SessionConfig struct {
	Client          llm.Client
	Model           string
	Tools           []builtintools.Tool
	Temperature     float64
	MaxTurns        int
	SystemPrompt    string
	VerboseLog      io.Writer
	InitialMessages []llm.Message
}

type RunResult struct {
	FinalResponse string
	Turns         int
	Messages      []llm.Message
}

type PromptOptions struct {
	ResponseMimeType string
	ResponseSchema   map[string]any
	DisableTools     bool
}

type Session struct {
	client      llm.Client
	model       string
	registry    *builtintools.Registry
	temperature float64
	maxTurns    int
	verboseLog  io.Writer
	messages    []llm.Message
}

func NewSession(cfg SessionConfig) (*Session, error) {
	if cfg.Client == nil {
		return nil, fmt.Errorf("client is required")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, fmt.Errorf("model is required")
	}
	if len(cfg.Tools) == 0 {
		return nil, fmt.Errorf("at least one tool is required")
	}
	if cfg.MaxTurns <= 0 {
		cfg.MaxTurns = 20
	}
	if strings.TrimSpace(cfg.SystemPrompt) == "" {
		cfg.SystemPrompt = DefaultSystemPrompt
	}

	initialMessages := cloneMessages(cfg.InitialMessages)
	if len(initialMessages) == 0 {
		initialMessages = []llm.Message{
			{Role: llm.RoleSystem, Text: cfg.SystemPrompt},
		}
	}

	return &Session{
		client:      cfg.Client,
		model:       cfg.Model,
		registry:    builtintools.NewRegistry(cfg.Tools),
		temperature: cfg.Temperature,
		maxTurns:    cfg.MaxTurns,
		verboseLog:  cfg.VerboseLog,
		messages:    initialMessages,
	}, nil
}

func (s *Session) Prompt(ctx context.Context, userPrompt string) (RunResult, error) {
	return s.PromptWithOptions(ctx, userPrompt, PromptOptions{})
}

func (s *Session) PromptWithOptions(ctx context.Context, userPrompt string, options PromptOptions) (RunResult, error) {
	if strings.TrimSpace(userPrompt) == "" {
		return RunResult{}, fmt.Errorf("user prompt cannot be empty")
	}

	s.messages = append(s.messages, llm.Message{Role: llm.RoleUser, Text: userPrompt})

	for turn := 1; turn <= s.maxTurns; turn++ {
		toolSpecs := s.registry.Specs()
		if options.DisableTools {
			toolSpecs = nil
		}

		resp, err := s.client.Complete(ctx, llm.CompletionRequest{
			Model:            s.model,
			Messages:         s.messages,
			Tools:            toolSpecs,
			Temperature:      s.temperature,
			ResponseMimeType: options.ResponseMimeType,
			ResponseSchema:   cloneMap(options.ResponseSchema),
		})
		if err != nil {
			return RunResult{Turns: turn - 1, Messages: cloneMessages(s.messages)}, err
		}

		assistantMsg := llm.Message{
			Role:      llm.RoleAssistant,
			Text:      resp.Text,
			Parts:     cloneAssistantParts(resp.Parts),
			ToolCalls: cloneToolCalls(resp.ToolCalls),
		}
		s.messages = append(s.messages, assistantMsg)

		s.logf("turn %d assistant text:\n%s\n", turn, strings.TrimSpace(resp.Text))

		if len(resp.ToolCalls) == 0 {
			return RunResult{
				FinalResponse: strings.TrimSpace(resp.Text),
				Turns:         turn,
				Messages:      cloneMessages(s.messages),
			}, nil
		}

		toolResults := make([]llm.ToolResult, 0, len(resp.ToolCalls))
		for _, call := range resp.ToolCalls {
			t, ok := s.registry.Get(call.Name)
			if !ok {
				result := llm.ToolResult{
					CallID:  call.ID,
					Name:    call.Name,
					Output:  fmt.Sprintf("unknown tool: %s", call.Name),
					IsError: true,
				}
				toolResults = append(toolResults, result)
				s.logf("tool %s: unknown tool\n", call.Name)
				continue
			}

			s.logf("tool %s args=%v\n", call.Name, call.Args)
			output, execErr := t.Execute(ctx, call.Args)
			toolResult := llm.ToolResult{
				CallID:  call.ID,
				Name:    call.Name,
				Output:  output,
				IsError: execErr != nil,
			}
			if execErr != nil {
				toolResult.Output = execErr.Error()
			}
			toolResults = append(toolResults, toolResult)
			s.logf("tool %s result (error=%v):\n%s\n", call.Name, toolResult.IsError, toolResult.Output)
		}
		s.messages = append(s.messages, llm.Message{Role: llm.RoleTool, ToolResults: cloneToolResults(toolResults)})
	}

	return RunResult{Turns: s.maxTurns, Messages: cloneMessages(s.messages)}, fmt.Errorf("max turns (%d) reached before completion", s.maxTurns)
}

// Run is kept for backwards compatibility; it delegates to Prompt.
func (s *Session) Run(ctx context.Context, userPrompt string) (RunResult, error) {
	return s.Prompt(ctx, userPrompt)
}

// Fork returns a deep copy of the session, including conversation history.
func (s *Session) Fork() *Session {
	cloned := *s
	cloned.messages = cloneMessages(s.messages)
	return &cloned
}

func (s *Session) Messages() []llm.Message {
	return cloneMessages(s.messages)
}

func (s *Session) logf(format string, args ...any) {
	if s.verboseLog == nil {
		return
	}
	fmt.Fprintf(s.verboseLog, format, args...)
}

func cloneMessages(in []llm.Message) []llm.Message {
	out := make([]llm.Message, 0, len(in))
	for _, m := range in {
		out = append(out, llm.Message{
			Role:        m.Role,
			Text:        m.Text,
			Parts:       cloneAssistantParts(m.Parts),
			ToolCalls:   cloneToolCalls(m.ToolCalls),
			ToolResults: cloneToolResults(m.ToolResults),
		})
	}
	return out
}

func cloneAssistantParts(in []llm.AssistantPart) []llm.AssistantPart {
	out := make([]llm.AssistantPart, 0, len(in))
	for _, p := range in {
		part := llm.AssistantPart{
			Text:             p.Text,
			ThoughtSignature: p.ThoughtSignature,
		}
		if p.ToolCall != nil {
			call := cloneToolCall(*p.ToolCall)
			part.ToolCall = &call
		}
		out = append(out, part)
	}
	return out
}

func cloneToolCalls(in []llm.ToolCall) []llm.ToolCall {
	out := make([]llm.ToolCall, 0, len(in))
	for _, call := range in {
		out = append(out, cloneToolCall(call))
	}
	return out
}

func cloneToolCall(call llm.ToolCall) llm.ToolCall {
	return llm.ToolCall{
		ID:               call.ID,
		Name:             call.Name,
		Args:             cloneMap(call.Args),
		ThoughtSignature: call.ThoughtSignature,
	}
}

func cloneToolResults(in []llm.ToolResult) []llm.ToolResult {
	out := make([]llm.ToolResult, 0, len(in))
	for _, r := range in {
		out = append(out, llm.ToolResult{
			CallID:  r.CallID,
			Name:    r.Name,
			Output:  r.Output,
			IsError: r.IsError,
		})
	}
	return out
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = cloneAny(v)
	}
	return out
}

func cloneAny(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return cloneMap(t)
	case []any:
		out := make([]any, len(t))
		for i := range t {
			out[i] = cloneAny(t[i])
		}
		return out
	default:
		return v
	}
}
