package llm

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestLiveSmokeGemini(t *testing.T) {
	apiKey := requireLiveProviderToken(t, "GEMINI_API_KEY")

	client := NewGeminiClient(apiKey)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	resp, err := client.Complete(ctx, CompletionRequest{
		Model:           "gemini-3-flash-preview",
		Messages:        []Message{{Role: RoleUser, Text: "Reply with a short acknowledgement."}},
		MaxOutputTokens: 256,
	})
	if err != nil {
		t.Fatalf("gemini live smoke failed: %v", err)
	}
	if strings.TrimSpace(resp.Text) == "" && len(resp.ToolCalls) == 0 {
		t.Fatalf("gemini returned empty response")
	}
}
