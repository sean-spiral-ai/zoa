package lmfrt

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	baselineagent "zoa/baselineagent"
)

func newLMFunctionTools(registry *Registry, manager *TaskManager) ([]baselineagent.Tool, error) {
	if registry == nil {
		return nil, fmt.Errorf("registry is nil")
	}
	if manager == nil {
		return nil, fmt.Errorf("task manager is nil")
	}
	return []baselineagent.Tool{
		&searchLMFunctionsTool{registry: registry},
		&searchLMMixinsTool{registry: registry},
		&loadLMMixinTool{registry: registry},
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
		Description: "Search registered LM Functions by keywords against id and when_to_use guidance (any keyword may match).",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"keywords": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "string",
					},
					"description": "Required keyword list. Each keyword is matched literally (case-insensitive substring, no glob/wildcards/regex) against id/when_to_use; any keyword may match.",
				},
				"limit": map[string]any{"type": "integer", "description": "Maximum results (default 10, max 50)"},
			},
			"required": []string{"keywords"},
		},
	}
}

func (t *searchLMFunctionsTool) Execute(_ context.Context, args map[string]any) (string, error) {
	keywords, err := keywordListArg(args, "keywords")
	if err != nil {
		return "", err
	}
	if len(keywords) == 0 {
		return "", fmt.Errorf("keywords must contain at least one non-empty string")
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
		ID           string         `json:"id"`
		WhenToUse    string         `json:"when_to_use"`
		InputSchema  map[string]any `json:"input_schema,omitempty"`
		OutputSchema map[string]any `json:"output_schema,omitempty"`
	}

	matches := []match{}
	for _, fn := range t.registry.List() {
		if !matchesAnyKeyword(fn.ID, fn.WhenToUse, keywords) {
			continue
		}
		matches = append(matches, match{
			ID:           fn.ID,
			WhenToUse:    fn.WhenToUse,
			InputSchema:  fn.InputSchema,
			OutputSchema: fn.OutputSchema,
		})
	}

	sort.Slice(matches, func(i, j int) bool { return matches[i].ID < matches[j].ID })
	if len(matches) > limit {
		matches = matches[:limit]
	}

	payload, err := json.MarshalIndent(map[string]any{
		"keywords": keywords,
		"count":    len(matches),
		"matches":  matches,
	}, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode results: %w", err)
	}
	return string(payload), nil
}

type callLMFunctionTool struct {
	manager *TaskManager
}

type searchLMMixinsTool struct {
	registry *Registry
}

func (t *searchLMMixinsTool) Spec() baselineagent.ToolSpec {
	return baselineagent.ToolSpec{
		Name:        "search_lmmixin",
		Description: "Search registered LM Mixins by keywords against id and when_to_use guidance (any keyword may match).",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"keywords": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "string",
					},
					"description": "Required keyword list. Each keyword is matched literally (case-insensitive substring, no glob/wildcards/regex) against id/when_to_use; any keyword may match.",
				},
				"limit": map[string]any{"type": "integer", "description": "Maximum results (default 10, max 50)"},
			},
			"required": []string{"keywords"},
		},
	}
}

func (t *searchLMMixinsTool) Execute(_ context.Context, args map[string]any) (string, error) {
	keywords, err := keywordListArg(args, "keywords")
	if err != nil {
		return "", err
	}
	if len(keywords) == 0 {
		return "", fmt.Errorf("keywords must contain at least one non-empty string")
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
		ID        string `json:"id"`
		WhenToUse string `json:"when_to_use"`
	}

	matches := []match{}
	for _, mixin := range t.registry.ListMixins() {
		if !matchesAnyKeyword(mixin.ID, mixin.WhenToUse, keywords) {
			continue
		}
		matches = append(matches, match{
			ID:        mixin.ID,
			WhenToUse: mixin.WhenToUse,
		})
	}

	sort.Slice(matches, func(i, j int) bool { return matches[i].ID < matches[j].ID })
	if len(matches) > limit {
		matches = matches[:limit]
	}

	payload, err := json.MarshalIndent(map[string]any{
		"keywords": keywords,
		"count":    len(matches),
		"matches":  matches,
	}, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode results: %w", err)
	}
	return string(payload), nil
}

type loadLMMixinTool struct {
	registry *Registry
}

func (t *loadLMMixinTool) Spec() baselineagent.ToolSpec {
	return baselineagent.ToolSpec{
		Name:        "load_lmmixin",
		Description: "Load a registered LM Mixin by id and return its raw content string.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"mixin_id": map[string]any{"type": "string", "description": "LM Mixin id"},
			},
			"required": []string{"mixin_id"},
		},
	}
}

func (t *loadLMMixinTool) Execute(_ context.Context, args map[string]any) (string, error) {
	mixinID, err := baselineagent.StringArg(args, "mixin_id", true)
	if err != nil {
		return "", err
	}
	mixinID = strings.TrimSpace(mixinID)
	mixin, ok := t.registry.GetMixin(mixinID)
	if !ok || mixin == nil {
		return "", fmt.Errorf("unknown mixin: %s", mixinID)
	}
	return mixin.Content, nil
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
				"timeout_sec": map[string]any{"type": "integer", "description": "Optional timeout in seconds for the entire spawned task duration; applies across all work in the task"},
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
	timeoutSec, err := baselineagent.IntArg(args, "timeout_sec", false)
	if err != nil {
		return "", err
	}
	if timeoutSec < 0 {
		return "", fmt.Errorf("timeout_sec must be >= 0")
	}

	input := map[string]any{}
	if raw, ok := args["input"]; ok && raw != nil {
		parsed, ok := raw.(map[string]any)
		if !ok {
			return "", fmt.Errorf("input must be an object")
		}
		input = cloneMapAny(parsed)
	}

	spawnOpts := SpawnOptions{}
	if timeoutSec > 0 {
		spawnOpts.TaskTimeout = time.Duration(timeoutSec) * time.Second
	}
	taskID, err := t.manager.Spawn(functionID, input, spawnOpts)
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

func keywordListArg(args map[string]any, key string) ([]string, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil, nil
	}
	switch list := raw.(type) {
	case []any:
		out := make([]string, 0, len(list))
		for i, item := range list {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("%s[%d] must be a string", key, i)
			}
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			out = append(out, strings.ToLower(s))
		}
		return out, nil
	case []string:
		out := make([]string, 0, len(list))
		for _, s := range list {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			out = append(out, strings.ToLower(s))
		}
		return out, nil
	default:
		return nil, fmt.Errorf("%s must be an array of strings", key)
	}
}

func matchesAnyKeyword(id string, whenToUse string, keywords []string) bool {
	if len(keywords) == 0 {
		return true
	}
	haystack := strings.ToLower(strings.TrimSpace(id) + "\n" + strings.TrimSpace(whenToUse))
	for _, kw := range keywords {
		if kw == "" {
			continue
		}
		if strings.Contains(haystack, kw) {
			return true
		}
	}
	return false
}
