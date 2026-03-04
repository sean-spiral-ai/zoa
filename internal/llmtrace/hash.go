package llmtrace

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
)

// RootHash is the well-known hash for the root sentinel node.
var RootHash = computeSHA256("ROOT")

func computeSHA256(data string) string {
	h := sha256.Sum256([]byte(data))
	return fmt.Sprintf("%x", h)
}

// NodeHash computes the content-addressable hash for a node:
// SHA256(parentHash + "\n" + canonicalMessageJSON).
func NodeHash(parentHash string, msgJSON []byte) string {
	return computeSHA256(parentHash + "\n" + string(msgJSON))
}

// CanonicalJSON produces a deterministic JSON encoding by round-tripping
// through json.Marshal which sorts map keys.
func CanonicalJSON(v any) ([]byte, error) {
	return json.Marshal(v)
}

// MessageSummary returns a short preview of a message for display.
func MessageSummary(role, text string, toolCalls []any, toolResults []any) string {
	if text != "" {
		if len(text) > 200 {
			return text[:200]
		}
		return text
	}
	if len(toolCalls) > 0 {
		if m, ok := toolCalls[0].(map[string]any); ok {
			if name, ok := m["Name"].(string); ok {
				return fmt.Sprintf("[tool_call: %s]", name)
			}
		}
		return "[tool_calls]"
	}
	if len(toolResults) > 0 {
		if m, ok := toolResults[0].(map[string]any); ok {
			if name, ok := m["Name"].(string); ok {
				return fmt.Sprintf("[tool_result: %s]", name)
			}
		}
		return "[tool_results]"
	}
	return ""
}
