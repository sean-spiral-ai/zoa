package builtintools

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"codexagentbase/baselineagent/internal/llm"
)

type GrepTool struct {
	paths *PathResolver
}

func NewGrepTool(paths *PathResolver) *GrepTool {
	return &GrepTool{paths: paths}
}

func (t *GrepTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        "grep",
		Description: "Search file contents for a pattern. Supports regex or literal search with optional context lines.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern":    map[string]any{"type": "string", "description": "Regex or literal pattern"},
				"path":       map[string]any{"type": "string", "description": "Directory or file path (default '.')"},
				"glob":       map[string]any{"type": "string", "description": "Optional file glob filter"},
				"ignoreCase": map[string]any{"type": "boolean"},
				"literal":    map[string]any{"type": "boolean"},
				"context":    map[string]any{"type": "integer", "description": "Context lines around each match"},
				"limit":      map[string]any{"type": "integer", "description": "Match limit (default 100)"},
			},
			"required": []string{"pattern"},
		},
	}
}

func (t *GrepTool) Execute(_ context.Context, args map[string]any) (string, error) {
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
	globPattern, err := StringArg(args, "glob", false)
	if err != nil {
		return "", err
	}
	ignoreCase, err := BoolArg(args, "ignoreCase", false)
	if err != nil {
		return "", err
	}
	literal, err := BoolArg(args, "literal", false)
	if err != nil {
		return "", err
	}
	contextLines, err := IntArg(args, "context", false)
	if err != nil {
		return "", err
	}
	if contextLines < 0 {
		contextLines = 0
	}
	limit, err := IntArg(args, "limit", false)
	if err != nil {
		return "", err
	}
	if limit <= 0 {
		limit = 100
	}

	abs, err := t.paths.Resolve(path)
	if err != nil {
		return "", err
	}

	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", path, err)
	}

	matcher, err := buildMatcher(pattern, literal, ignoreCase)
	if err != nil {
		return "", err
	}

	results := make([]string, 0, min(limit, 200)*2)
	matchCount := 0
	limitHit := false
	lineTruncated := false

	searchFile := func(filePath string, rel string) error {
		data, err := os.ReadFile(filePath)
		if err != nil || isLikelyBinary(data) {
			return nil
		}
		lines := splitLines(string(data))
		for i, line := range lines {
			if matcher(line) {
				matchCount++
				blockStart := max(1, i+1-contextLines)
				blockEnd := min(len(lines), i+1+contextLines)
				for ln := blockStart; ln <= blockEnd; ln++ {
					text := lines[ln-1]
					if len(text) > 500 {
						text = text[:500] + "... [truncated]"
						lineTruncated = true
					}
					if ln == i+1 {
						results = append(results, fmt.Sprintf("%s:%d: %s", rel, ln, text))
					} else {
						results = append(results, fmt.Sprintf("%s-%d- %s", rel, ln, text))
					}
				}
				if matchCount >= limit {
					limitHit = true
					return fs.SkipAll
				}
			}
		}
		return nil
	}

	if !info.IsDir() {
		rel := filepath.Base(abs)
		if err := searchFile(abs, rel); err != nil && err != fs.SkipAll {
			return "", err
		}
	} else {
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
			if globPattern != "" {
				ok, err := doublestar.PathMatch(globPattern, relSlash)
				if err != nil {
					return err
				}
				if !ok {
					return nil
				}
			}
			if err := searchFile(current, relSlash); err != nil {
				return err
			}
			if limitHit {
				return fs.SkipAll
			}
			return nil
		})
		if err != nil && err != fs.SkipAll {
			return "", err
		}
	}

	if len(results) == 0 {
		return "No matches found", nil
	}

	sort.Strings(results)
	output := strings.Join(results, "\n")
	tr := TruncateHead(output, 1_000_000, DefaultMaxBytes)
	output = tr.Content

	notices := make([]string, 0, 3)
	if limitHit {
		notices = append(notices, fmt.Sprintf("%d match limit reached", limit))
	}
	if tr.Truncated {
		notices = append(notices, fmt.Sprintf("output truncated by %s", tr.Reason))
	}
	if lineTruncated {
		notices = append(notices, "some lines truncated to 500 chars")
	}
	if len(notices) > 0 {
		output += "\n\n[" + strings.Join(notices, ". ") + "]"
	}
	return output, nil
}

func buildMatcher(pattern string, literal, ignoreCase bool) (func(string) bool, error) {
	if literal {
		needle := pattern
		if ignoreCase {
			needle = strings.ToLower(needle)
			return func(line string) bool {
				return strings.Contains(strings.ToLower(line), needle)
			}, nil
		}
		return func(line string) bool { return strings.Contains(line, needle) }, nil
	}
	flags := ""
	if ignoreCase {
		flags = "(?i)"
	}
	re, err := regexp.Compile(flags + pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid regex pattern: %w", err)
	}
	return func(line string) bool { return re.MatchString(line) }, nil
}

func splitLines(s string) []string {
	r := strings.NewReader(strings.ReplaceAll(s, "\r\n", "\n"))
	scanner := bufio.NewScanner(r)
	lines := []string{}
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if bytes.HasSuffix([]byte(s), []byte("\n")) {
		lines = append(lines, "")
	}
	return lines
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
