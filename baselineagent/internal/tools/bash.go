package tools

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"codexagentbase/baselineagent/internal/llm"
)

type BashTool struct {
	paths *PathResolver
}

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
	cancel := func() {}
	if timeoutSec > 0 {
		runCtx, cancel = context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	}
	defer cancel()

	cmd := exec.CommandContext(runCtx, "bash", "-lc", cmdStr)
	cmd.Dir = t.paths.Root()

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	err = cmd.Run()
	output := strings.TrimSpace(buf.String())
	if output == "" {
		output = "(no output)"
	}

	tr := TruncateTail(output, DefaultMaxLines, DefaultMaxBytes)
	result := tr.Content
	if tr.Truncated {
		result += fmt.Sprintf("\n\n[Output truncated by %s. Showing last chunk of %s total.]", tr.Reason, formatSize(tr.TotalBytes))
	}

	if runCtx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("command timed out after %d seconds\n%s", timeoutSec, result)
	}
	if err != nil {
		return "", fmt.Errorf("command failed: %w\n%s", err, result)
	}

	return result, nil
}
