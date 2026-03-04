package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"

	builtintools "zoa/baselineagent/builtintools"
	"zoa/baselineagent/internal/llm"
	"zoa/internal/semtrace"
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

const toolArgsTracePreviewMax = 2000

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
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(userPrompt) == "" {
		return RunResult{}, fmt.Errorf("user prompt cannot be empty")
	}
	promptPreview := previewText(userPrompt, 1000)
	ctx, promptRegion := semtrace.StartRegionWithAttrs(ctx, "baselineagent.prompt", map[string]any{
		"model":          s.model,
		"tools_disabled": options.DisableTools,
		"prompt_len":     len(userPrompt),
		"prompt_preview": promptPreview,
	})
	defer promptRegion.End()
	semtrace.LogAttrs(ctx, "prompt", "prompt started", map[string]any{
		"model":          s.model,
		"tools_disabled": options.DisableTools,
		"prompt_len":     len(userPrompt),
		"prompt_preview": promptPreview,
	})

	s.messages = append(s.messages, llm.Message{Role: llm.RoleUser, Text: userPrompt})

	for turn := 1; turn <= s.maxTurns; turn++ {
		turnCtx, turnRegion := semtrace.StartRegionWithAttrs(ctx, fmt.Sprintf("baselineagent.turn.%d", turn), map[string]any{
			"turn":          turn,
			"message_count": len(s.messages),
		})
		semtrace.LogAttrs(turnCtx, "turn", "turn started", map[string]any{
			"turn":          turn,
			"message_count": len(s.messages),
		})
		toolSpecs := s.registry.Specs()
		if options.DisableTools {
			toolSpecs = nil
		}

		llmCtx, llmRegion := semtrace.StartRegionWithAttrs(turnCtx, "llm.complete", map[string]any{
			"turn":  turn,
			"model": s.model,
		})
		resp, err := s.client.Complete(llmCtx, llm.CompletionRequest{
			Model:            s.model,
			Messages:         s.messages,
			Tools:            toolSpecs,
			Temperature:      s.temperature,
			ResponseMimeType: options.ResponseMimeType,
			ResponseSchema:   cloneMap(options.ResponseSchema),
		})
		llmRegion.End()
		if err != nil {
			semtrace.LogAttrs(llmCtx, "llm.error", err.Error(), map[string]any{
				"turn":  turn,
				"model": s.model,
			})
			turnRegion.End()
			return RunResult{Turns: turn - 1, Messages: cloneMessages(s.messages)}, err
		}
		semtrace.LogAttrs(llmCtx, "llm.complete", "llm complete", map[string]any{
			"turn":       turn,
			"text_len":   len(resp.Text),
			"tool_calls": len(resp.ToolCalls),
		})

		assistantMsg := llm.Message{
			Role:      llm.RoleAssistant,
			Text:      resp.Text,
			Parts:     cloneAssistantParts(resp.Parts),
			ToolCalls: cloneToolCalls(resp.ToolCalls),
		}
		s.messages = append(s.messages, assistantMsg)

		s.logf("turn %d assistant text:\n%s\n", turn, strings.TrimSpace(resp.Text))

		if len(resp.ToolCalls) == 0 {
			turnRegion.End()
			return RunResult{
				FinalResponse: strings.TrimSpace(resp.Text),
				Turns:         turn,
				Messages:      cloneMessages(s.messages),
			}, nil
		}

		toolResults := make([]llm.ToolResult, 0, len(resp.ToolCalls))
		for _, call := range resp.ToolCalls {
			argsPreview, argsLen, argsTruncated := previewJSON(call.Args, toolArgsTracePreviewMax)
			toolCtx, toolRegion := semtrace.StartRegionWithAttrs(turnCtx, "tool."+call.Name, map[string]any{
				"turn":           turn,
				"call_id":        call.ID,
				"tool":           call.Name,
				"args_preview":   argsPreview,
				"args_len":       argsLen,
				"args_truncated": argsTruncated,
			})
			semtrace.LogAttrs(toolCtx, "tool.start", "tool call started", map[string]any{
				"turn":           turn,
				"call_id":        call.ID,
				"tool":           call.Name,
				"args_preview":   argsPreview,
				"args_len":       argsLen,
				"args_truncated": argsTruncated,
			})
			slog.Debug(
				"tool call",
				"component", "baselineagent_toolcall",
				"phase", "start",
				"turn", turn,
				"call_id", call.ID,
				"tool", call.Name,
				"args", call.Args,
			)
			t, ok := s.registry.Get(call.Name)
			if !ok {
				result := llm.ToolResult{
					CallID:  call.ID,
					Name:    call.Name,
					Output:  fmt.Sprintf("unknown tool: %s", call.Name),
					IsError: true,
				}
				slog.Debug(
					"tool call",
					"component", "baselineagent_toolcall",
					"phase", "end",
					"turn", turn,
					"call_id", call.ID,
					"tool", call.Name,
					"error", true,
					"error_type", "unknown_tool",
					"output_len", len(result.Output),
					"output_preview", previewText(result.Output, 300),
				)
				toolResults = append(toolResults, result)
				s.logf("tool %s: unknown tool\n", call.Name)
				semtrace.LogAttrs(toolCtx, "tool.error", "unknown tool", map[string]any{
					"turn":    turn,
					"call_id": call.ID,
					"tool":    call.Name,
				})
				toolRegion.End()
				continue
			}

			s.logf("tool %s args=%v\n", call.Name, call.Args)
			output, execErr := t.Execute(toolCtx, call.Args)
			toolResult := llm.ToolResult{
				CallID:  call.ID,
				Name:    call.Name,
				Output:  output,
				IsError: execErr != nil,
			}
			if execErr != nil {
				toolResult.Output = execErr.Error()
				semtrace.LogAttrs(toolCtx, "tool.error", execErr.Error(), map[string]any{
					"turn":    turn,
					"call_id": call.ID,
					"tool":    call.Name,
				})
			}
			slog.Debug(
				"tool call",
				"component", "baselineagent_toolcall",
				"phase", "end",
				"turn", turn,
				"call_id", call.ID,
				"tool", call.Name,
				"error", execErr != nil,
				"output_len", len(toolResult.Output),
				"output_preview", previewText(toolResult.Output, 300),
			)
			toolResults = append(toolResults, toolResult)
			s.logf("tool %s result (error=%v):\n%s\n", call.Name, toolResult.IsError, toolResult.Output)
			semtrace.LogAttrs(toolCtx, "tool.end", "tool call finished", map[string]any{
				"turn":       turn,
				"call_id":    call.ID,
				"tool":       call.Name,
				"error":      execErr != nil,
				"output_len": len(toolResult.Output),
			})
			toolRegion.End()
		}
		s.messages = append(s.messages, llm.Message{Role: llm.RoleTool, ToolResults: cloneToolResults(toolResults)})
		turnRegion.End()
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

func previewText(s string, maxLen int) string {
	if maxLen <= 0 {
		maxLen = 300
	}
	s = strings.ReplaceAll(s, "\n", "\\n")
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "...(truncated)"
}

func previewJSON(v any, maxLen int) (preview string, totalLen int, truncated bool) {
	if maxLen <= 0 {
		maxLen = 2000
	}
	raw, err := json.Marshal(v)
	if err != nil {
		fallback := fmt.Sprintf("%v", v)
		return previewText(fallback, maxLen), len(fallback), len(fallback) > maxLen
	}
	s := string(raw)
	if len(s) <= maxLen {
		return s, len(s), false
	}
	return s[:maxLen] + "...(truncated)", len(s), true
}
