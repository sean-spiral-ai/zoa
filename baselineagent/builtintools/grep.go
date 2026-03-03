package builtintools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"zoa/baselineagent/internal/llm"
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
		Description: "Search file contents using ripgrep (fallback: grep). Supports regex/literal patterns and optional context. Refuses searching '/'.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern":    map[string]any{"type": "string", "description": "Regex or literal pattern"},
				"path":       map[string]any{"type": "string", "description": "Directory or file path (default '.'). Root '/' is rejected."},
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

func (t *GrepTool) Execute(ctx context.Context, args map[string]any) (string, error) {
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
	if filepath.Clean(abs) == string(filepath.Separator) {
		return "", fmt.Errorf("refusing to grep '/' (root). choose a narrower path")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	output, cmdCaptureTruncated, err := runGrepBackend(ctx, grepRunOptions{
		AbsPath:      abs,
		Pattern:      pattern,
		Glob:         globPattern,
		IgnoreCase:   ignoreCase,
		Literal:      literal,
		ContextLines: contextLines,
		Limit:        limit,
	})
	if err != nil {
		if errors.Is(err, errNoMatches) {
			return "No matches found", nil
		}
		return "", err
	}

	tr := TruncateHead(output, 1_000_000, DefaultMaxBytes)
	output = tr.Content

	notices := make([]string, 0, 2)
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

type grepRunOptions struct {
	AbsPath      string
	Pattern      string
	Glob         string
	IgnoreCase   bool
	Literal      bool
	ContextLines int
	Limit        int
}

var errNoMatches = errors.New("no matches")

const maxGrepCapturedBytes = 2 * 1024 * 1024

func runGrepBackend(ctx context.Context, opts grepRunOptions) (string, bool, error) {
	backend, args, err := buildGrepCommand(opts)
	if err != nil {
		return "", false, err
	}
	cmd := exec.CommandContext(ctx, backend, args...)
	cwd := opts.AbsPath
	if info, statErr := os.Stat(opts.AbsPath); statErr == nil && !info.IsDir() {
		cwd = filepath.Dir(opts.AbsPath)
	}
	cmd.Dir = cwd

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
			return "", captureTruncated, errNoMatches
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
		return "", captureTruncated, errNoMatches
	}
	return output, captureTruncated, nil
}

func buildGrepCommand(opts grepRunOptions) (string, []string, error) {
	if _, err := os.Stat(opts.AbsPath); err != nil {
		return "", nil, fmt.Errorf("stat path: %w", err)
	}
	if rgPath, err := exec.LookPath("rg"); err == nil {
		return rgPath, buildRGArgs(opts), nil
	}
	if grepPath, err := exec.LookPath("grep"); err == nil {
		return grepPath, buildGrepArgs(opts), nil
	}
	return "", nil, fmt.Errorf("neither ripgrep (rg) nor grep is available")
}

func buildRGArgs(opts grepRunOptions) []string {
	args := []string{"--line-number", "--no-heading", "--color=never"}
	if opts.Literal {
		args = append(args, "-F")
	}
	if opts.IgnoreCase {
		args = append(args, "-i")
	}
	if opts.ContextLines > 0 {
		args = append(args, "-C", strconv.Itoa(opts.ContextLines))
	}
	if opts.Limit > 0 {
		args = append(args, "--max-count", strconv.Itoa(opts.Limit))
	}
	args = append(args, "--glob", "!.git/**", "--glob", "!node_modules/**")
	if strings.TrimSpace(opts.Glob) != "" {
		args = append(args, "--glob", strings.TrimSpace(opts.Glob))
	}
	args = append(args, opts.Pattern, opts.AbsPath)
	return args
}

func buildGrepArgs(opts grepRunOptions) []string {
	args := []string{"-n", "-I"}
	if opts.Literal {
		args = append(args, "-F")
	}
	if opts.IgnoreCase {
		args = append(args, "-i")
	}
	if opts.ContextLines > 0 {
		args = append(args, "-C", strconv.Itoa(opts.ContextLines))
	}
	if opts.Limit > 0 {
		args = append(args, "-m", strconv.Itoa(opts.Limit))
	}
	if strings.TrimSpace(opts.Glob) != "" {
		args = append(args, "--include="+strings.TrimSpace(opts.Glob))
	}

	if info, err := os.Stat(opts.AbsPath); err == nil && info.IsDir() {
		args = append(args, "-R", "--exclude-dir=.git", "--exclude-dir=node_modules")
	}
	args = append(args, opts.Pattern, opts.AbsPath)
	return args
}

type cappedBuffer struct {
	maxBytes int
	total    int
	buf      bytes.Buffer
}

func newCappedBuffer(maxBytes int) *cappedBuffer {
	if maxBytes <= 0 {
		maxBytes = maxGrepCapturedBytes
	}
	return &cappedBuffer{maxBytes: maxBytes}
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.total += len(p)
	remaining := b.maxBytes - b.buf.Len()
	if remaining > 0 {
		if len(p) > remaining {
			_, _ = b.buf.Write(p[:remaining])
		} else {
			_, _ = b.buf.Write(p)
		}
	}
	return len(p), nil
}

func (b *cappedBuffer) String() string {
	return b.buf.String()
}

func (b *cappedBuffer) Truncated() bool {
	return b.total > b.maxBytes
}
