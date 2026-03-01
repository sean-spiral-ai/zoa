package lmf

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	baselineagent "codexagentbase/baselineagent"
)

func NewLMFunctionTools(registry *Registry, manager *TaskManager) ([]baselineagent.Tool, error) {
	if registry == nil {
		return nil, fmt.Errorf("registry is nil")
	}
	if manager == nil {
		return nil, fmt.Errorf("task manager is nil")
	}
	return []baselineagent.Tool{
		&searchLMFunctionsTool{registry: registry},
		&callLMFunctionTool{manager: manager},
		&waitLMFunctionTool{manager: manager},
	}, nil
}

type searchLMFunctionsTool struct {
	registry *Registry
}

func (t *searchLMFunctionsTool) Spec() baselineagent.ToolSpec {
	return baselineagent.ToolSpec{
		Name:        "search_lmfunctions",
		Description: "Search registered LM Functions by id and when_to_use guidance.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "Free-text search query (optional)"},
				"limit": map[string]any{"type": "integer", "description": "Maximum results (default 10, max 50)"},
			},
		},
	}
}

func (t *searchLMFunctionsTool) Execute(_ context.Context, args map[string]any) (string, error) {
	query, err := baselineagent.StringArg(args, "query", false)
	if err != nil {
		return "", err
	}
	limit, err := baselineagent.IntArg(args, "limit", false)
	if err != nil {
		return "", err
	}
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}

	type match struct {
		ID        string         `json:"id"`
		WhenToUse string         `json:"when_to_use"`
		Schema    map[string]any `json:"schema,omitempty"`
	}

	matches := []match{}
	q := strings.ToLower(strings.TrimSpace(query))
	for _, fn := range t.registry.List() {
		if q != "" {
			if !strings.Contains(strings.ToLower(fn.ID), q) && !strings.Contains(strings.ToLower(fn.WhenToUse), q) {
				continue
			}
		}
		matches = append(matches, match{ID: fn.ID, WhenToUse: fn.WhenToUse, Schema: fn.Schema})
	}

	sort.Slice(matches, func(i, j int) bool { return matches[i].ID < matches[j].ID })
	if len(matches) > limit {
		matches = matches[:limit]
	}

	payload, err := json.MarshalIndent(map[string]any{
		"query":   query,
		"count":   len(matches),
		"matches": matches,
	}, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode results: %w", err)
	}
	return string(payload), nil
}

type callLMFunctionTool struct {
	manager *TaskManager
}

func (t *callLMFunctionTool) Spec() baselineagent.ToolSpec {
	return baselineagent.ToolSpec{
		Name:        "call_lmfunction",
		Description: "Start an LM Function task asynchronously and return a task_id handle.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"function_id": map[string]any{"type": "string", "description": "LM Function id"},
				"input":       map[string]any{"type": "object", "description": "Optional function input object"},
			},
			"required": []string{"function_id"},
		},
	}
}

func (t *callLMFunctionTool) Execute(_ context.Context, args map[string]any) (string, error) {
	functionID, err := baselineagent.StringArg(args, "function_id", true)
	if err != nil {
		return "", err
	}

	input := map[string]any{}
	if raw, ok := args["input"]; ok && raw != nil {
		parsed, ok := raw.(map[string]any)
		if !ok {
			return "", fmt.Errorf("input must be an object")
		}
		input = cloneMapAny(parsed)
	}

	taskID, err := t.manager.Spawn(functionID, input)
	if err != nil {
		return "", err
	}

	payload, err := json.MarshalIndent(map[string]any{
		"task_id": taskID,
		"status":  TaskStatusRunning,
	}, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode response: %w", err)
	}
	return string(payload), nil
}

type waitLMFunctionTool struct {
	manager *TaskManager
}

func (t *waitLMFunctionTool) Spec() baselineagent.ToolSpec {
	return baselineagent.ToolSpec{
		Name:        "wait_lmfunction",
		Description: "Wait for an LM Function task by task_id.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id":     map[string]any{"type": "string", "description": "Task handle returned by call_lmfunction"},
				"timeout_sec": map[string]any{"type": "integer", "description": "Optional wait timeout in seconds"},
			},
			"required": []string{"task_id"},
		},
	}
}

func (t *waitLMFunctionTool) Execute(_ context.Context, args map[string]any) (string, error) {
	taskID, err := baselineagent.StringArg(args, "task_id", true)
	if err != nil {
		return "", err
	}
	timeoutSec, err := baselineagent.IntArg(args, "timeout_sec", false)
	if err != nil {
		return "", err
	}
	timeout := time.Duration(timeoutSec) * time.Second

	snapshot, timedOut, err := t.manager.Wait(taskID, timeout)
	if err != nil {
		return "", err
	}

	payload := map[string]any{
		"timed_out": timedOut,
		"task":      snapshot,
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode response: %w", err)
	}
	return string(b), nil
}
