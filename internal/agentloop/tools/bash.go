package builtintools

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"zoa/internal/agentloop/llm"
)

type BashTool struct {
	paths *PathResolver
}

const maxBashCapturedBytes = 2 * 1024 * 1024

func NewBashTool(paths *PathResolver) *BashTool {
	return &BashTool{paths: paths}
}

func (t *BashTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        "bash",
		Description: "Run a shell command in the workspace root. Returns combined stdout/stderr with truncation for long output.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string", "description": "Shell command"},
				"timeout": map[string]any{"type": "integer", "description": "Timeout in seconds"},
			},
			"required": []string{"command"},
		},
	}
}

func (t *BashTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	cmdStr, err := StringArg(args, "command", true)
	if err != nil {
		return "", err
	}
	timeoutSec, err := IntArg(args, "timeout", false)
	if err != nil {
		return "", err
	}

	runCtx := ctx
	if runCtx == nil {
		runCtx = context.Background()
	}
	cancel := func() {}
	if timeoutSec > 0 {
		runCtx, cancel = context.WithTimeout(runCtx, time.Duration(timeoutSec)*time.Second)
	}
	defer cancel()

	cmd := exec.CommandContext(runCtx, "bash", "-lc", cmdStr)
	cmd.Dir = t.paths.Root()

	capture := newTailBuffer(maxBashCapturedBytes)
	cmd.Stdout = capture
	cmd.Stderr = capture

	err = cmd.Run()
	output := strings.TrimSpace(capture.String())
	if output == "" {
		output = "(no output)"
	}

	tr := TruncateTail(output, DefaultMaxLines, DefaultMaxBytes)
	result := tr.Content
	notices := make([]string, 0, 2)
	if tr.Truncated {
		notices = append(notices, fmt.Sprintf("Output truncated by %s. Showing last chunk of %s total.", tr.Reason, formatSize(capture.TotalBytes())))
	}
	if capture.Truncated() {
		notices = append(notices, "command output capture limit reached")
	}
	if len(notices) > 0 {
		result += "\n\n[" + strings.Join(notices, " ") + "]"
	}

	if runCtx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("command timed out after %d seconds\n%s", timeoutSec, result)
	}
	if err != nil {
		return "", fmt.Errorf("command failed: %w\n%s", err, result)
	}

	return result, nil
}

type tailBuffer struct {
	mu    sync.Mutex
	max   int
	total int
	buf   []byte
}

func newTailBuffer(maxBytes int) *tailBuffer {
	if maxBytes <= 0 {
		maxBytes = maxBashCapturedBytes
	}
	return &tailBuffer{
		max: maxBytes,
		buf: make([]byte, 0, maxBytes),
	}
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.total += len(p)
	if b.max <= 0 {
		return len(p), nil
	}
	if len(p) >= b.max {
		b.buf = append(b.buf[:0], p[len(p)-b.max:]...)
		return len(p), nil
	}

	drop := len(b.buf) + len(p) - b.max
	if drop > 0 {
		copy(b.buf, b.buf[drop:])
		b.buf = b.buf[:len(b.buf)-drop]
	}
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

func (b *tailBuffer) TotalBytes() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.total
}

func (b *tailBuffer) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.total > b.max
}
