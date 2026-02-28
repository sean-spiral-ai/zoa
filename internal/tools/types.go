package tools

import (
	"context"
	"fmt"

	"codexagentbase/internal/llm"
)

type Tool interface {
	Spec() llm.ToolSpec
	Execute(ctx context.Context, args map[string]any) (string, error)
}

type Registry struct {
	byName map[string]Tool
	order  []Tool
}

func NewRegistry(tools []Tool) *Registry {
	byName := make(map[string]Tool, len(tools))
	for _, t := range tools {
		name := t.Spec().Name
		byName[name] = t
	}
	return &Registry{byName: byName, order: tools}
}

func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.byName[name]
	return t, ok
}

func (r *Registry) Specs() []llm.ToolSpec {
	specs := make([]llm.ToolSpec, 0, len(r.order))
	for _, t := range r.order {
		specs = append(specs, t.Spec())
	}
	return specs
}

func StringArg(args map[string]any, key string, required bool) (string, error) {
	raw, ok := args[key]
	if !ok {
		if required {
			return "", fmt.Errorf("missing required field: %s", key)
		}
		return "", nil
	}
	s, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", key)
	}
	if required && s == "" {
		return "", fmt.Errorf("%s cannot be empty", key)
	}
	return s, nil
}

func IntArg(args map[string]any, key string, required bool) (int, error) {
	raw, ok := args[key]
	if !ok {
		if required {
			return 0, fmt.Errorf("missing required field: %s", key)
		}
		return 0, nil
	}
	switch n := raw.(type) {
	case float64:
		return int(n), nil
	case int:
		return n, nil
	default:
		return 0, fmt.Errorf("%s must be a number", key)
	}
}

func BoolArg(args map[string]any, key string, required bool) (bool, error) {
	raw, ok := args[key]
	if !ok {
		if required {
			return false, fmt.Errorf("missing required field: %s", key)
		}
		return false, nil
	}
	b, ok := raw.(bool)
	if !ok {
		return false, fmt.Errorf("%s must be a boolean", key)
	}
	return b, nil
}
