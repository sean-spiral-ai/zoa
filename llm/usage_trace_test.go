package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"zoa/internal/semtrace"
)

func TestGeminiComplete_EmitsUsageAttrsOnRegionEnd(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"candidates": []any{
				map[string]any{
					"content": map[string]any{
						"parts": []any{map[string]any{"text": "hello"}},
					},
				},
			},
			"usageMetadata": map[string]any{
				"promptTokenCount":        11,
				"candidatesTokenCount":    7,
				"totalTokenCount":         18,
				"cachedContentTokenCount": 3,
			},
		})
	}))
	defer server.Close()

	client := NewGeminiClient("test-key")
	client.baseURL = server.URL
	client.httpClient = server.Client()

	semtrace.Global().Start()
	_, err := client.Complete(context.Background(), CompletionRequest{
		Model: "gemini-2.0-flash",
		Messages: []Message{{
			Role: RoleUser,
			Text: "ping",
		}},
	})
	if err != nil {
		t.Fatalf("complete failed: %v", err)
	}
	dump := semtrace.Global().StopAndDump()

	attrs := mustFindRegionEndAttrs(t, dump, "llm.gemini.complete")
	assertIntAttr(t, attrs, "tokens.input", 11)
	assertIntAttr(t, attrs, "tokens.output", 7)
	assertIntAttr(t, attrs, "tokens.total", 18)
	assertIntAttr(t, attrs, "tokens.cache_read_input", 3)
}

func TestAnthropicComplete_EmitsUsageAttrsOnRegionEnd(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "msg_1",
			"type":  "message",
			"role":  "assistant",
			"model": "claude-sonnet-4-5",
			"content": []any{
				map[string]any{"type": "text", "text": "hello"},
			},
			"stop_reason": "end_turn",
			"usage": map[string]any{
				"input_tokens":                101,
				"output_tokens":               13,
				"cache_creation_input_tokens": 88,
				"cache_read_input_tokens":     9,
			},
		})
	}))
	defer server.Close()

	client := NewAnthropicClientWithOAuthToken("test-token")
	client.url = server.URL
	client.httpClient = server.Client()

	semtrace.Global().Start()
	_, err := client.Complete(context.Background(), CompletionRequest{
		Model: "claude-sonnet-4-5",
		Messages: []Message{{
			Role: RoleUser,
			Text: "ping",
		}},
	})
	if err != nil {
		t.Fatalf("complete failed: %v", err)
	}
	dump := semtrace.Global().StopAndDump()

	attrs := mustFindRegionEndAttrs(t, dump, "llm.anthropic.complete")
	assertIntAttr(t, attrs, "tokens.input", 101)
	assertIntAttr(t, attrs, "tokens.output", 13)
	assertIntAttr(t, attrs, "tokens.cache_creation_input", 88)
	assertIntAttr(t, attrs, "tokens.cache_read_input", 9)
}

func mustFindRegionEndAttrs(t *testing.T, dump semtrace.Dump, regionName string) map[string]any {
	t.Helper()
	startByID := map[uint64]semtrace.Event{}
	for _, ev := range dump.Events {
		if ev.Kind == "region_start" {
			startByID[ev.RegionID] = ev
		}
	}
	for _, ev := range dump.Events {
		if ev.Kind != "region_end" {
			continue
		}
		start, ok := startByID[ev.RegionID]
		if !ok {
			continue
		}
		if start.Name == regionName {
			return ev.Attrs
		}
	}
	t.Fatalf("region_end attrs for %q not found", regionName)
	return nil
}

func assertIntAttr(t *testing.T, attrs map[string]any, key string, want int) {
	t.Helper()
	if attrs == nil {
		t.Fatalf("attrs nil; expected %s=%d", key, want)
	}
	gotAny, ok := attrs[key]
	if !ok {
		t.Fatalf("missing attr %q", key)
	}
	gotFloat, ok := gotAny.(float64)
	if ok {
		if int(gotFloat) != want {
			t.Fatalf("attr %q mismatch: got %v want %d", key, gotAny, want)
		}
		return
	}
	gotInt, ok := gotAny.(int)
	if ok {
		if gotInt != want {
			t.Fatalf("attr %q mismatch: got %d want %d", key, gotInt, want)
		}
		return
	}
	t.Fatalf("attr %q has unexpected type %T value=%v", key, gotAny, gotAny)
}
