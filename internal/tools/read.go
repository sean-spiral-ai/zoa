package tools

import (
	"context"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"codexagentbase/internal/llm"
)

type ReadTool struct {
	paths *PathResolver
}

func NewReadTool(paths *PathResolver) *ReadTool {
	return &ReadTool{paths: paths}
}

func (t *ReadTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        "read",
		Description: "Read text file content with optional line offset/limit. Output is truncated for large files.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":   map[string]any{"type": "string", "description": "File path, relative to workspace root"},
				"offset": map[string]any{"type": "integer", "description": "1-based line offset"},
				"limit":  map[string]any{"type": "integer", "description": "Max lines to include"},
			},
			"required": []string{"path"},
		},
	}
}

func (t *ReadTool) Execute(_ context.Context, args map[string]any) (string, error) {
	path, err := StringArg(args, "path", true)
	if err != nil {
		return "", err
	}
	offset, err := IntArg(args, "offset", false)
	if err != nil {
		return "", err
	}
	limit, err := IntArg(args, "limit", false)
	if err != nil {
		return "", err
	}

	abs, err := t.paths.Resolve(path)
	if err != nil {
		return "", err
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	if isLikelyBinary(data) {
		return "File appears to be binary; read tool only returns text files.", nil
	}

	text := string(data)
	lines := strings.Split(text, "\n")
	start := 0
	if offset > 0 {
		start = offset - 1
	}
	if start < 0 {
		start = 0
	}
	if start >= len(lines) {
		return "", fmt.Errorf("offset %d is past end of file (%d lines)", offset, len(lines))
	}

	selected := lines[start:]
	if limit > 0 && limit < len(selected) {
		selected = selected[:limit]
	}

	content := strings.Join(selected, "\n")
	tr := TruncateHead(content, DefaultMaxLines, DefaultMaxBytes)
	output := tr.Content
	if tr.Truncated {
		nextOffset := start + strings.Count(tr.Content, "\n") + 2
		output += fmt.Sprintf("\n\n[Output truncated by %s; total file lines: %d. Use offset=%d to continue.]", tr.Reason, len(lines), nextOffset)
	} else if limit > 0 && (start+limit) < len(lines) {
		nextOffset := start + limit + 1
		output += fmt.Sprintf("\n\n[%d more lines available. Use offset=%d to continue.]", len(lines)-(start+limit), nextOffset)
	}

	if output == "" {
		output = "(empty file)"
	}
	return output, nil
}

func isLikelyBinary(data []byte) bool {
	sample := data
	if len(sample) > 8192 {
		sample = sample[:8192]
	}
	if !utf8.Valid(sample) {
		return true
	}
	for _, b := range sample {
		if b == 0 {
			return true
		}
	}
	return false
}
