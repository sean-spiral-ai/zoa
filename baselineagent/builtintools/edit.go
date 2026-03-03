package builtintools

import (
	"context"
	"fmt"
	"os"
	"strings"

	"zoa/baselineagent/internal/llm"
)

type EditTool struct {
	paths *PathResolver
}

const maxEditFileBytes = 2 * 1024 * 1024

func NewEditTool(paths *PathResolver) *EditTool {
	return &EditTool{paths: paths}
}

func (t *EditTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        "edit",
		Description: "Replace exact oldText with newText in a text file up to 2MB. oldText must match exactly and uniquely.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string", "description": "File path"},
				"oldText": map[string]any{"type": "string", "description": "Exact text to replace"},
				"newText": map[string]any{"type": "string", "description": "Replacement text"},
			},
			"required": []string{"path", "oldText", "newText"},
		},
	}
}

func (t *EditTool) Execute(_ context.Context, args map[string]any) (string, error) {
	path, err := StringArg(args, "path", true)
	if err != nil {
		return "", err
	}
	oldText, err := StringArg(args, "oldText", true)
	if err != nil {
		return "", err
	}
	newText, err := StringArg(args, "newText", true)
	if err != nil {
		return "", err
	}
	abs, err := t.paths.Resolve(path)
	if err != nil {
		return "", err
	}

	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", path, err)
	}
	if info.Size() > maxEditFileBytes {
		return "", fmt.Errorf("refusing to edit %s: file is %s (max %s)", path, formatSize(int(info.Size())), formatSize(maxEditFileBytes))
	}

	contentBytes, err := os.ReadFile(abs)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	if len(contentBytes) > maxEditFileBytes {
		return "", fmt.Errorf("refusing to edit %s: file is %s (max %s)", path, formatSize(len(contentBytes)), formatSize(maxEditFileBytes))
	}
	if isLikelyBinary(contentBytes) {
		return "", fmt.Errorf("refusing to edit %s: file appears to be binary", path)
	}
	content := string(contentBytes)

	occurrences := strings.Count(content, oldText)
	if occurrences == 0 {
		return "", fmt.Errorf("oldText not found in %s", path)
	}
	if occurrences > 1 {
		return "", fmt.Errorf("oldText appears %d times in %s; provide a more specific snippet", occurrences, path)
	}

	updated := strings.Replace(content, oldText, newText, 1)
	if updated == content {
		return "", fmt.Errorf("replacement made no changes in %s", path)
	}

	if err := os.WriteFile(abs, []byte(updated), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return fmt.Sprintf("Edited %s (replaced 1 occurrence).", path), nil
}
