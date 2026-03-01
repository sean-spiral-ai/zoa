package lmf

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func Run(ctx context.Context, fn *Function, input map[string]any) (RunResult, error) {
	if fn == nil {
		return RunResult{}, fmt.Errorf("function is nil")
	}
	if fn.Exec == nil {
		return RunResult{}, fmt.Errorf("function %q has nil Exec", fn.ID)
	}
	if input == nil {
		input = map[string]any{}
	}

	taskCtx, err := NewTaskContext(ctx, taskContextOptionsFromInput(input))
	if err != nil {
		return RunResult{}, err
	}

	result := RunResult{FunctionID: fn.ID}
	output, err := fn.Exec(taskCtx, input)
	result.Conversation = taskCtx.conversationHistory()
	if output != nil {
		result.Output = output
	}
	if err != nil {
		return result, err
	}
	return result, nil
}

func taskContextOptionsFromInput(input map[string]any) TaskContextOptions {
	opts := TaskContextOptions{}
	if v, ok := input["cwd"].(string); ok {
		opts.CWD = strings.TrimSpace(v)
	}
	if v, ok := input["model"].(string); ok {
		opts.Model = strings.TrimSpace(v)
	}
	if v, ok := numberAsInt(input["max_turns"]); ok {
		opts.MaxTurns = v
	}
	if v, ok := numberAsInt(input["timeout_sec"]); ok && v > 0 {
		opts.Timeout = time.Duration(v) * time.Second
	}
	if v, ok := numberAsFloat(input["temperature"]); ok {
		opts.Temperature = v
	}
	return opts
}

func numberAsInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case float64:
		return int(n), true
	default:
		return 0, false
	}
}

func numberAsFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	default:
		return 0, false
	}
}
