package builtintools

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"zoa/llm"
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

	f, err := os.Open(abs)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	defer f.Close()

	sample := make([]byte, 8192)
	n, sampleErr := f.Read(sample)
	if sampleErr != nil && sampleErr != io.EOF {
		return "", fmt.Errorf("read %s: %w", path, sampleErr)
	}
	if isLikelyBinary(sample[:n]) {
		return "File appears to be binary; read tool only returns text files.", nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("seek %s: %w", path, err)
	}

	startLine := 1
	if offset > 0 {
		startLine = offset
	}
	if startLine < 1 {
		startLine = 1
	}

	var builder strings.Builder
	reader := bufio.NewReader(f)
	totalLines := 0
	includedLines := 0
	includedBytes := 0
	lastIncludedLine := startLine - 1
	endedWithNewline := false
	truncated := false
	truncReason := ""

	appendLine := func(line string, lineNo int) {
		if lineNo < startLine {
			return
		}
		if limit > 0 && (lineNo-startLine+1) > limit {
			return
		}
		if truncated {
			return
		}
		if includedLines >= DefaultMaxLines {
			truncated = true
			truncReason = "lines"
			return
		}

		lineBytes := len(line)
		if includedLines > 0 {
			lineBytes++
		}
		if includedBytes+lineBytes > DefaultMaxBytes {
			truncated = true
			truncReason = "bytes"
			return
		}

		if includedLines > 0 {
			builder.WriteByte('\n')
			includedBytes++
		}
		builder.WriteString(line)
		includedBytes += len(line)
		includedLines++
		lastIncludedLine = lineNo
	}

	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil && readErr != io.EOF {
			return "", fmt.Errorf("read %s: %w", path, readErr)
		}
		if readErr == io.EOF && line == "" {
			break
		}

		hasNewline := strings.HasSuffix(line, "\n")
		if hasNewline {
			line = strings.TrimSuffix(line, "\n")
		}
		line = strings.TrimSuffix(line, "\r")

		totalLines++
		appendLine(line, totalLines)
		endedWithNewline = hasNewline

		if readErr == io.EOF {
			break
		}
	}

	// Match strings.Split behavior for trailing newline and empty file.
	if totalLines == 0 || endedWithNewline {
		totalLines++
		appendLine("", totalLines)
	}

	if startLine > totalLines {
		return "", fmt.Errorf("offset %d is past end of file (%d lines)", offset, totalLines)
	}

	output := builder.String()
	if truncated {
		nextOffset := lastIncludedLine + 1
		if includedLines == 0 {
			nextOffset = startLine + 1
		}
		output += fmt.Sprintf("\n\n[Output truncated by %s; total file lines: %d. Use offset=%d to continue.]", truncReason, totalLines, nextOffset)
	} else if limit > 0 && (startLine-1+limit) < totalLines {
		nextOffset := startLine + limit
		remaining := totalLines - (startLine - 1 + limit)
		output += fmt.Sprintf("\n\n[%d more lines available. Use offset=%d to continue.]", remaining, nextOffset)
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
