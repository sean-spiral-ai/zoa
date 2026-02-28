package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"codexagentbase/baselineagent/internal/llm"
)

type WriteTool struct {
	paths *PathResolver
}

func NewWriteTool(paths *PathResolver) *WriteTool {
	return &WriteTool{paths: paths}
}

func (t *WriteTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        "write",
		Description: "Write a file. Creates parent directories and overwrites existing content.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string", "description": "File path, relative to workspace root"},
				"content": map[string]any{"type": "string", "description": "Full file content"},
			},
			"required": []string{"path", "content"},
		},
	}
}

func (t *WriteTool) Execute(_ context.Context, args map[string]any) (string, error) {
	path, err := StringArg(args, "path", true)
	if err != nil {
		return "", err
	}
	content, err := StringArg(args, "content", true)
	if err != nil {
		return "", err
	}

	abs, err := t.paths.Resolve(path)
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", fmt.Errorf("create parent directories for %s: %w", path, err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return fmt.Sprintf("Wrote %d bytes to %s", len(content), path), nil
}
