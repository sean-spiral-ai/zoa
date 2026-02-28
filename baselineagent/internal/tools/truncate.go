package tools

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	DefaultMaxLines = 2000
	DefaultMaxBytes = 50 * 1024
)

type Truncation struct {
	Content    string
	Truncated  bool
	Reason     string
	TotalLines int
	TotalBytes int
}

func TruncateHead(content string, maxLines, maxBytes int) Truncation {
	if maxLines <= 0 {
		maxLines = DefaultMaxLines
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}

	lines := strings.Split(content, "\n")
	totalBytes := len([]byte(content))

	if len(lines) <= maxLines && totalBytes <= maxBytes {
		return Truncation{Content: content, Truncated: false, TotalLines: len(lines), TotalBytes: totalBytes}
	}

	var out []string
	bytesUsed := 0
	reason := "lines"
	for i, line := range lines {
		if i >= maxLines {
			reason = "lines"
			break
		}
		lineBytes := len([]byte(line))
		if len(out) > 0 {
			lineBytes += 1
		}
		if bytesUsed+lineBytes > maxBytes {
			reason = "bytes"
			break
		}
		out = append(out, line)
		bytesUsed += lineBytes
	}

	return Truncation{
		Content:    strings.Join(out, "\n"),
		Truncated:  true,
		Reason:     reason,
		TotalLines: len(lines),
		TotalBytes: totalBytes,
	}
}

func TruncateTail(content string, maxLines, maxBytes int) Truncation {
	if maxLines <= 0 {
		maxLines = DefaultMaxLines
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}

	lines := strings.Split(content, "\n")
	totalBytes := len([]byte(content))

	if len(lines) <= maxLines && totalBytes <= maxBytes {
		return Truncation{Content: content, Truncated: false, TotalLines: len(lines), TotalBytes: totalBytes}
	}

	out := make([]string, 0, maxLines)
	bytesUsed := 0
	reason := "lines"
	for i := len(lines) - 1; i >= 0; i-- {
		if len(out) >= maxLines {
			reason = "lines"
			break
		}
		line := lines[i]
		lineBytes := len([]byte(line))
		if len(out) > 0 {
			lineBytes += 1
		}
		if bytesUsed+lineBytes > maxBytes {
			reason = "bytes"
			if len(out) == 0 {
				out = append(out, trimToLastBytes(line, maxBytes))
			}
			break
		}
		out = append(out, line)
		bytesUsed += lineBytes
	}

	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}

	return Truncation{
		Content:    strings.Join(out, "\n"),
		Truncated:  true,
		Reason:     reason,
		TotalLines: len(lines),
		TotalBytes: totalBytes,
	}
}

func trimToLastBytes(s string, maxBytes int) string {
	b := []byte(s)
	if len(b) <= maxBytes {
		return s
	}
	start := len(b) - maxBytes
	for start < len(b) && !utf8.Valid(b[start:]) {
		start++
	}
	if start >= len(b) {
		return ""
	}
	return string(b[start:])
}

func formatSize(bytes int) string {
	if bytes < 1024 {
		return fmt.Sprintf("%dB", bytes)
	}
	if bytes < 1024*1024 {
		return fmt.Sprintf("%.1fKB", float64(bytes)/1024)
	}
	return fmt.Sprintf("%.1fMB", float64(bytes)/(1024*1024))
}
