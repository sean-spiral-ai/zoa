package baselineagent

import (
	"context"
	"fmt"
	"io"
	"time"

	topmodel "zoa/model"
)

const (
	DefaultGeminiModel    = topmodel.DefaultGeminiModel
	DefaultAnthropicModel = topmodel.DefaultAnthropicModel
	DefaultModel          = topmodel.DefaultModel
	DefaultTemperature    = topmodel.DefaultTemperature
	DefaultMaxTurns       = topmodel.DefaultMaxTurns
)

type RunConfig struct {
	CWD             string
	Instruction     string
	Model           string
	MaxTurns        int
	Timeout         time.Duration
	Temperature     float64
	VerboseLog      io.Writer
	SystemPrompt    string
	Tools           []Tool
	InitialMessages []ConversationMessage
}

type RunResult struct {
	FinalResponse string
	Turns         int
	ToolCalls     int
	MessageCount  int
}

type Runner interface {
	Run(ctx context.Context, cfg RunConfig) (RunResult, error)
}

type defaultRunner struct {
	apiKey string
}

func NewDefaultRunner(apiKey string) Runner {
	return &defaultRunner{apiKey: apiKey}
}

func (r *defaultRunner) Run(ctx context.Context, cfg RunConfig) (RunResult, error) {
	if cfg.Instruction == "" {
		return RunResult{}, fmt.Errorf("instruction is required")
	}
	conversation, err := NewConversation(r.apiKey, ConversationConfig{
		CWD:             cfg.CWD,
		Model:           cfg.Model,
		MaxTurns:        cfg.MaxTurns,
		Timeout:         cfg.Timeout,
		Temperature:     cfg.Temperature,
		VerboseLog:      cfg.VerboseLog,
		SystemPrompt:    cfg.SystemPrompt,
		Tools:           cfg.Tools,
		InitialMessages: cfg.InitialMessages,
	})
	if err != nil {
		return RunResult{}, err
	}
	return conversation.Prompt(ctx, cfg.Instruction)
}

func Run(ctx context.Context, apiKey string, cfg RunConfig) (RunResult, error) {
	return NewDefaultRunner(apiKey).Run(ctx, cfg)
}
