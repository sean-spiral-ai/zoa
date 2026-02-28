package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"codexagentbase/baseline-agent/internal/llm"
)

type LsTool struct {
	paths *PathResolver
}

func NewLsTool(paths *PathResolver) *LsTool {
	return &LsTool{paths: paths}
}

func (t *LsTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        "ls",
		Description: "List directory entries, sorted alphabetically. Directories end with '/'.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":  map[string]any{"type": "string", "description": "Directory path (default '.')"},
				"limit": map[string]any{"type": "integer", "description": "Max entries to return (default 500)"},
			},
		},
	}
}

func (t *LsTool) Execute(_ context.Context, args map[string]any) (string, error) {
	path, err := StringArg(args, "path", false)
	if err != nil {
		return "", err
	}
	if path == "" {
		path = "."
	}
	limit, err := IntArg(args, "limit", false)
	if err != nil {
		return "", err
	}
	if limit <= 0 {
		limit = 500
	}

	abs, err := t.paths.Resolve(path)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return "", fmt.Errorf("read dir %s: %w", path, err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		return strings.ToLower(names[i]) < strings.ToLower(names[j])
	})

	limitHit := false
	if len(names) > limit {
		names = names[:limit]
		limitHit = true
	}
	if len(names) == 0 {
		return "(empty directory)", nil
	}

	output := strings.Join(names, "\n")
	tr := TruncateHead(output, 1_000_000, DefaultMaxBytes)
	output = tr.Content

	notices := make([]string, 0, 2)
	if limitHit {
		notices = append(notices, fmt.Sprintf("%d entry limit reached", limit))
	}
	if tr.Truncated {
		notices = append(notices, fmt.Sprintf("output truncated by %s", tr.Reason))
	}
	if len(notices) > 0 {
		output += "\n\n[" + strings.Join(notices, ". ") + "]"
	}

	if rel, err := filepath.Rel(t.paths.Root(), abs); err == nil && rel != "." {
		output = fmt.Sprintf("Listing %s\n\n%s", rel, output)
	}

	return output, nil
}
