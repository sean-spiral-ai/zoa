package agent

import (
	"context"
	"fmt"
	"io"
	"strings"

	"codexagentbase/internal/llm"
	"codexagentbase/internal/tools"
)

type SessionConfig struct {
	Client       llm.Client
	Model        string
	Tools        []tools.Tool
	Temperature  float64
	MaxTurns     int
	SystemPrompt string
	VerboseLog   io.Writer
}

type RunResult struct {
	FinalResponse string
	Turns         int
	Messages      []llm.Message
}

type Session struct {
	client       llm.Client
	model        string
	registry     *tools.Registry
	temperature  float64
	maxTurns     int
	systemPrompt string
	verboseLog   io.Writer
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

	return &Session{
		client:       cfg.Client,
		model:        cfg.Model,
		registry:     tools.NewRegistry(cfg.Tools),
		temperature:  cfg.Temperature,
		maxTurns:     cfg.MaxTurns,
		systemPrompt: cfg.SystemPrompt,
		verboseLog:   cfg.VerboseLog,
	}, nil
}

func (s *Session) Run(ctx context.Context, userPrompt string) (RunResult, error) {
	messages := []llm.Message{
		{Role: llm.RoleSystem, Text: s.systemPrompt},
		{Role: llm.RoleUser, Text: userPrompt},
	}

	for turn := 1; turn <= s.maxTurns; turn++ {
		resp, err := s.client.Complete(ctx, llm.CompletionRequest{
			Model:       s.model,
			Messages:    messages,
			Tools:       s.registry.Specs(),
			Temperature: s.temperature,
		})
		if err != nil {
			return RunResult{Turns: turn - 1, Messages: messages}, err
		}

		assistantMsg := llm.Message{
			Role:      llm.RoleAssistant,
			Text:      resp.Text,
			Parts:     resp.Parts,
			ToolCalls: resp.ToolCalls,
		}
		messages = append(messages, assistantMsg)

		s.logf("turn %d assistant text:\n%s\n", turn, strings.TrimSpace(resp.Text))

		if len(resp.ToolCalls) == 0 {
			return RunResult{
				FinalResponse: strings.TrimSpace(resp.Text),
				Turns:         turn,
				Messages:      messages,
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
		messages = append(messages, llm.Message{Role: llm.RoleTool, ToolResults: toolResults})
	}

	return RunResult{Turns: s.maxTurns, Messages: messages}, fmt.Errorf("max turns (%d) reached before completion", s.maxTurns)
}

func (s *Session) logf(format string, args ...any) {
	if s.verboseLog == nil {
		return
	}
	fmt.Fprintf(s.verboseLog, format, args...)
}
