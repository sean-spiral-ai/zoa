package builtintools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"zoa/baselineagent/internal/llm"
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
		Description: "Find files by glob pattern using fd (fallback: find). Supports **. Ignores .git and node_modules.",
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

func (t *FindTool) Execute(ctx context.Context, args map[string]any) (string, error) {
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
	if ctx == nil {
		ctx = context.Background()
	}

	rawOutput, cmdCaptureTruncated, err := runFindBackend(ctx, findRunOptions{
		AbsPath: abs,
		Pattern: pattern,
	})
	if err != nil {
		if errors.Is(err, errNoFindMatches) {
			return "No files found matching pattern", nil
		}
		return "", err
	}

	matches := normalizeFindOutput(rawOutput)
	limitHit := len(matches) > limit
	if limitHit {
		matches = matches[:limit]
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
	if cmdCaptureTruncated {
		notices = append(notices, "command output capture limit reached")
	}
	if len(notices) > 0 {
		output += "\n\n[" + strings.Join(notices, ". ") + "]"
	}
	return output, nil
}

type findRunOptions struct {
	AbsPath string
	Pattern string
}

var errNoFindMatches = errors.New("no files found")

func runFindBackend(ctx context.Context, opts findRunOptions) (string, bool, error) {
	backend, args, cmdDir, err := buildFindCommand(opts)
	if err != nil {
		return "", false, err
	}
	cmd := exec.CommandContext(ctx, backend, args...)
	cmd.Dir = cmdDir

	stdout := newCappedBuffer(maxGrepCapturedBytes)
	stderr := newCappedBuffer(maxGrepCapturedBytes / 2)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	runErr := cmd.Run()
	output := strings.TrimSpace(stdout.String())
	errOut := strings.TrimSpace(stderr.String())
	captureTruncated := stdout.Truncated() || stderr.Truncated()

	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) && exitErr.ExitCode() == 1 && output == "" && errOut == "" {
			return "", captureTruncated, errNoFindMatches
		}
		detail := strings.TrimSpace(errOut)
		if detail == "" {
			detail = strings.TrimSpace(output)
		}
		if detail != "" {
			return "", captureTruncated, fmt.Errorf("%s failed: %w\n%s", backend, runErr, detail)
		}
		return "", captureTruncated, fmt.Errorf("%s failed: %w", backend, runErr)
	}

	if output == "" {
		return "", captureTruncated, errNoFindMatches
	}
	return output, captureTruncated, nil
}

func buildFindCommand(opts findRunOptions) (backend string, args []string, cmdDir string, err error) {
	info, err := os.Stat(opts.AbsPath)
	if err != nil {
		return "", nil, "", fmt.Errorf("stat path: %w", err)
	}

	cmdDir = opts.AbsPath
	searchRoot := "."
	if !info.IsDir() {
		cmdDir = filepath.Dir(opts.AbsPath)
		searchRoot = filepath.Base(opts.AbsPath)
	}

	if fdPath, fdErr := exec.LookPath("fd"); fdErr == nil {
		return fdPath, buildFDArgs(opts.Pattern, searchRoot), cmdDir, nil
	}
	if fdPath, fdErr := exec.LookPath("fdfind"); fdErr == nil {
		return fdPath, buildFDArgs(opts.Pattern, searchRoot), cmdDir, nil
	}
	if findPath, findErr := exec.LookPath("find"); findErr == nil {
		return findPath, buildFindArgs(opts.Pattern, searchRoot), cmdDir, nil
	}
	return "", nil, "", fmt.Errorf("neither fd/fdfind nor find is available")
}

func buildFDArgs(pattern, searchRoot string) []string {
	return []string{
		"--glob",
		"--type", "f",
		"--color", "never",
		"--hidden",
		"--no-ignore",
		"--exclude", ".git",
		"--exclude", "node_modules",
		pattern,
		searchRoot,
	}
}

func buildFindArgs(pattern, searchRoot string) []string {
	pred := []string{"-name", pattern}
	if strings.Contains(pattern, "/") {
		pred = []string{"-path", "./" + pattern}
	}
	args := []string{
		searchRoot,
		"-type", "d", "(", "-name", ".git", "-o", "-name", "node_modules", ")", "-prune",
		"-o", "-type", "f",
	}
	args = append(args, pred...)
	args = append(args, "-print")
	return args
}

func normalizeFindOutput(out string) []string {
	if strings.TrimSpace(out) == "" {
		return nil
	}
	lines := strings.Split(out, "\n")
	results := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		line = strings.TrimPrefix(line, "./")
		line = filepath.ToSlash(line)
		if line == "." {
			continue
		}
		results = append(results, line)
	}
	return results
}
