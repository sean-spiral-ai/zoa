package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"zoa/llm"
	tools "zoa/tools"
)

func newZoaFunctionTools(registry *Registry, manager *TaskManager) ([]tools.Tool, error) {
	if registry == nil {
		return nil, fmt.Errorf("registry is nil")
	}
	if manager == nil {
		return nil, fmt.Errorf("task manager is nil")
	}
	return []tools.Tool{
		&searchZoaFunctionsTool{registry: registry},
		&searchZoaMixinsTool{registry: registry},
		&loadZoaMixinTool{registry: registry},
		&callZoaFunctionTool{manager: manager},
		&waitZoaFunctionTool{manager: manager},
		&killZoaFunctionTool{manager: manager},
	}, nil
}

type searchZoaFunctionsTool struct {
	registry *Registry
}

func (t *searchZoaFunctionsTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        "search_zoafunctions",
		Description: "Search registered ZoaFunctions by keywords against id and when_to_use guidance (any keyword may match).",
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

func (t *searchZoaFunctionsTool) Execute(_ context.Context, args map[string]any) (string, error) {
	keywords, err := keywordListArg(args, "keywords")
	if err != nil {
		return "", err
	}
	if len(keywords) == 0 {
		return "", fmt.Errorf("keywords must contain at least one non-empty string")
	}
	limit, err := tools.IntArg(args, "limit", false)
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

type callZoaFunctionTool struct {
	manager *TaskManager
}

type searchZoaMixinsTool struct {
	registry *Registry
}

func (t *searchZoaMixinsTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        "search_zoamixins",
		Description: "Search registered ZoaMixins by keywords against id and when_to_use guidance (any keyword may match).",
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

func (t *searchZoaMixinsTool) Execute(_ context.Context, args map[string]any) (string, error) {
	keywords, err := keywordListArg(args, "keywords")
	if err != nil {
		return "", err
	}
	if len(keywords) == 0 {
		return "", fmt.Errorf("keywords must contain at least one non-empty string")
	}
	limit, err := tools.IntArg(args, "limit", false)
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

type loadZoaMixinTool struct {
	registry *Registry
}

func (t *loadZoaMixinTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        "load_zoamixin",
		Description: "Load a registered ZoaMixin by id and return its raw content string.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"mixin_id": map[string]any{"type": "string", "description": "ZoaMixin id"},
			},
			"required": []string{"mixin_id"},
		},
	}
}

func (t *loadZoaMixinTool) Execute(_ context.Context, args map[string]any) (string, error) {
	mixinID, err := tools.StringArg(args, "mixin_id", true)
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

func (t *callZoaFunctionTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        "call_zoafunction",
		Description: "Start a ZoaFunction task asynchronously and return a task_id handle. For long-running tasks, use wait_zoafunction with a timeout and call kill_zoafunction if you need to cancel.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"function_id": map[string]any{"type": "string", "description": "ZoaFunction id"},
				"input":       map[string]any{"type": "object", "description": "Optional function input object"},
			},
			"required": []string{"function_id"},
		},
	}
}

func (t *callZoaFunctionTool) Execute(_ context.Context, args map[string]any) (string, error) {
	functionID, err := tools.StringArg(args, "function_id", true)
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

	taskID, err := t.manager.Spawn(functionID, input, SpawnOptions{})
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

type waitZoaFunctionTool struct {
	manager *TaskManager
}

func (t *waitZoaFunctionTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        "wait_zoafunction",
		Description: "Wait for a ZoaFunction task by task_id. If timeout is reached, the task keeps running; call wait_zoafunction again for long-running work or use kill_zoafunction to cancel.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id":     map[string]any{"type": "string", "description": "Task handle returned by call_zoafunction"},
				"timeout_sec": map[string]any{"type": "integer", "description": "Optional wait timeout in seconds. If exceeded, timed_out=true and task continues running."},
			},
			"required": []string{"task_id"},
		},
	}
}

func (t *waitZoaFunctionTool) Execute(_ context.Context, args map[string]any) (string, error) {
	taskID, err := tools.StringArg(args, "task_id", true)
	if err != nil {
		return "", err
	}
	timeoutSec, err := tools.IntArg(args, "timeout_sec", false)
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

type killZoaFunctionTool struct {
	manager *TaskManager
}

func (t *killZoaFunctionTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        "kill_zoafunction",
		Description: "Cancel a running ZoaFunction task by task_id.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{"type": "string", "description": "Task handle returned by call_zoafunction"},
			},
			"required": []string{"task_id"},
		},
	}
}

func (t *killZoaFunctionTool) Execute(_ context.Context, args map[string]any) (string, error) {
	taskID, err := tools.StringArg(args, "task_id", true)
	if err != nil {
		return "", err
	}
	cancelRequested, err := t.manager.Cancel(taskID)
	if err != nil {
		return "", err
	}
	snapshot, err := t.manager.Get(taskID)
	if err != nil {
		return "", err
	}
	payload := map[string]any{
		"cancel_requested": cancelRequested,
		"task":             snapshot,
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
