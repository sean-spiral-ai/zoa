package tools

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"codexagentbase/baselineagent/internal/llm"
)

type FindTool struct {
	paths *PathResolver
}

func NewFindTool(paths *PathResolver) *FindTool {
	return &FindTool{paths: paths}
}

func (t *FindTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        "find",
		Description: "Find files by glob pattern (supports **). Ignores .git and node_modules.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{"type": "string", "description": "Glob like '*.go' or 'cmd/**/*.go'"},
				"path":    map[string]any{"type": "string", "description": "Search root (default '.')"},
				"limit":   map[string]any{"type": "integer", "description": "Max results (default 1000)"},
			},
			"required": []string{"pattern"},
		},
	}
}

func (t *FindTool) Execute(_ context.Context, args map[string]any) (string, error) {
	pattern, err := StringArg(args, "pattern", true)
	if err != nil {
		return "", err
	}
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
		limit = 1000
	}

	abs, err := t.paths.Resolve(path)
	if err != nil {
		return "", err
	}

	matches := make([]string, 0, min(limit, 200))
	limitHit := false
	err = filepath.WalkDir(abs, func(current string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		rel, err := filepath.Rel(abs, current)
		if err != nil {
			return nil
		}
		relSlash := filepath.ToSlash(rel)

		if d.IsDir() {
			if relSlash == ".git" || relSlash == "node_modules" || strings.HasPrefix(relSlash, ".git/") || strings.HasPrefix(relSlash, "node_modules/") {
				return fs.SkipDir
			}
			return nil
		}

		ok, err := doublestar.PathMatch(pattern, relSlash)
		if err != nil {
			return err
		}
		if !ok {
			baseOk, _ := doublestar.PathMatch(pattern, filepath.Base(relSlash))
			if !baseOk {
				return nil
			}
		}

		if len(matches) >= limit {
			limitHit = true
			return fs.SkipAll
		}
		matches = append(matches, relSlash)
		return nil
	})
	if err != nil {
		return "", err
	}

	if len(matches) == 0 {
		return "No files found matching pattern", nil
	}
	sort.Strings(matches)

	output := strings.Join(matches, "\n")
	tr := TruncateHead(output, 1_000_000, DefaultMaxBytes)
	output = tr.Content

	notices := make([]string, 0, 2)
	if limitHit {
		notices = append(notices, fmt.Sprintf("%d result limit reached", limit))
	}
	if tr.Truncated {
		notices = append(notices, fmt.Sprintf("output truncated by %s", tr.Reason))
	}
	if len(notices) > 0 {
		output += "\n\n[" + strings.Join(notices, ". ") + "]"
	}
	return output, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
