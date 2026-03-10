//go:build liveapi

package liveapi

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	convdb "zoa/conversation/db"
	"zoa/internal/agentloop/llm"
	"zoa/internal/agentloop/model"
	"zoa/internal/gatewayclient"
	"zoa/internal/keys"
)

func TestGatewaySmokeUsesDiverseIdeationZoaFunction(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live smoke test in -short mode")
	}

	modelName := requireLiveModel(t)
	sessionDir := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	client, err := gatewayclient.NewLocalGatewayClient(gatewayclient.LocalConfig{
		Session:     gatewayclient.DefaultSession,
		SessionDir:  sessionDir,
		CWD:         cwd,
		Model:       modelName,
		MaxTurns:    24,
		Temperature: 0.2,
		TimeoutSec:  180,
	})
	if err != nil {
		t.Fatalf("new local gateway client: %v", err)
	}
	defer func() {
		_ = client.Close()
	}()

	prompt := strings.TrimSpace(`
Use the diverse_ideation.diverse_ideation ZoaFunction to come up with exactly 5 silly hats that an AI model could wear when out on the town.

Return the final answer as exactly 5 lines.
Format each line as:
1. <hat name> | <short description>

Do not include any intro or outro text.
`)

	enqueue, err := client.Enqueue(prompt, "gatewaychannel://test")
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if !enqueue.Accepted {
		t.Fatalf("enqueue not accepted: %+v", enqueue)
	}

	reply := waitForOutboxReply(t, client, enqueue.InboundID, 2*time.Minute)
	if count := countParseableIdeas(reply.Text); count != 5 {
		t.Fatalf("parseable idea count = %d, want 5\nreply:\n%s", count, reply.Text)
	}

	db, err := convdb.Open(filepath.Join(sessionDir, "conversation.db"))
	if err != nil {
		t.Fatalf("open conversation db: %v", err)
	}
	defer func() {
		_ = db.Close()
	}()

	refs, err := db.ListRefs()
	if err != nil {
		t.Fatalf("list refs: %v", err)
	}

	sessionRefName := "sessions/" + client.Session()
	sessionRef, err := db.GetRef(sessionRefName)
	if err != nil {
		t.Fatalf("get session ref: %v", err)
	}
	if sessionRef.Hash == convdb.RootHash {
		t.Fatalf("session ref still points at root")
	}

	var matchingTaskRefs []convdb.Ref
	for _, ref := range refs {
		if !strings.HasPrefix(ref.Name, "tasks/") || !strings.HasSuffix(ref.Name, "/main") {
			continue
		}
		chain, err := db.LoadChain(ref.Hash)
		if err != nil {
			t.Fatalf("load task chain %q: %v", ref.Name, err)
		}
		if chainHasText(chain, "silly hats") || chainHasText(chain, "out on the town") {
			matchingTaskRefs = append(matchingTaskRefs, ref)
		}
	}
	if len(matchingTaskRefs) == 0 {
		t.Fatalf("did not find task conversation ref for diverse_ideation in refs: %+v", refs)
	}
	if len(matchingTaskRefs) != 1 {
		t.Fatalf("matching task ref count = %d, want 1: %+v", len(matchingTaskRefs), matchingTaskRefs)
	}
	taskRef := matchingTaskRefs[0]
	if taskRef.Hash == convdb.RootHash {
		t.Fatalf("task ref still points at root: %+v", taskRef)
	}
	if taskRef.Hash == sessionRef.Hash {
		t.Fatalf("session and task refs unexpectedly share the same head hash: %s", taskRef.Hash)
	}

	sessionChain, err := db.LoadChain(sessionRef.Hash)
	if err != nil {
		t.Fatalf("load session chain: %v", err)
	}
	taskChain, err := db.LoadChain(taskRef.Hash)
	if err != nil {
		t.Fatalf("load task chain: %v", err)
	}
	if len(sessionChain) == 0 {
		t.Fatalf("session chain unexpectedly empty")
	}
	if len(taskChain) == 0 {
		t.Fatalf("task chain unexpectedly empty")
	}
	if !chainHasToolCall(sessionChain, "call_zoafunction") {
		t.Fatalf("session chain missing call_zoafunction tool call")
	}
	if !chainHasToolResult(sessionChain, "call_zoafunction") {
		t.Fatalf("session chain missing call_zoafunction tool result")
	}
	if !chainHasToolCall(sessionChain, "wait_zoafunction") {
		t.Fatalf("session chain missing wait_zoafunction tool call")
	}
	if !chainHasText(taskChain, "silly hats") {
		t.Fatalf("task chain missing silly hats prompt")
	}
}

func requireLiveModel(t *testing.T) string {
	t.Helper()

	if token := strings.TrimSpace(keys.ResolveWithNearestDotEnv("", model.GeminiAPIKeyEnvVar)); token != "" {
		return model.ModelGemini3FlashPreview
	}
	if token := strings.TrimSpace(keys.ResolveWithNearestDotEnv("", model.AnthropicOAuthTokenEnvVar)); token != "" {
		return model.ModelClaudeSonnet46
	}
	t.Skip("no live model credential found; set GEMINI_API_KEY or ANTHROPIC_OAUTH_TOKEN")
	return ""
}

func waitForOutboxReply(t *testing.T, client gatewayclient.GatewayClient, inboundID int64, timeout time.Duration) gatewayclient.OutboxMessage {
	t.Helper()

	deadline := time.Now().Add(timeout)
	lastID := int64(0)
	for time.Now().Before(deadline) {
		messages, maxID, err := client.OutboxSince(lastID, 20)
		if err != nil {
			t.Fatalf("outbox since: %v", err)
		}
		lastID = maxID
		for _, msg := range messages {
			if msg.InReplyTo == inboundID {
				return msg
			}
		}
		time.Sleep(2 * time.Second)
	}
	t.Fatalf("timed out waiting for outbox reply to inbound_id=%d", inboundID)
	return gatewayclient.OutboxMessage{}
}

func countParseableIdeas(text string) int {
	re := regexp.MustCompile(`(?m)^\s*([1-5])\.\s+[^|\n]+\|\s+.+$`)
	matches := re.FindAllStringSubmatch(text, -1)
	seen := map[string]struct{}{}
	for _, match := range matches {
		if len(match) > 1 {
			seen[match[1]] = struct{}{}
		}
	}
	return len(seen)
}

func chainHasText(chain []convdb.Node, needle string) bool {
	needle = strings.ToLower(strings.TrimSpace(needle))
	for _, node := range chain {
		if strings.Contains(strings.ToLower(node.Message.Text), needle) {
			return true
		}
		for _, part := range node.Message.Parts {
			if strings.Contains(strings.ToLower(part.Text), needle) {
				return true
			}
		}
	}
	return false
}

func chainHasToolCall(chain []convdb.Node, toolName string) bool {
	for _, node := range chain {
		for _, call := range node.Message.ToolCalls {
			if call.Name == toolName {
				return true
			}
		}
		for _, part := range node.Message.Parts {
			if part.ToolCall != nil && part.ToolCall.Name == toolName {
				return true
			}
		}
	}
	return false
}

func chainHasToolResult(chain []convdb.Node, toolName string) bool {
	for _, node := range chain {
		for _, result := range node.Message.ToolResults {
			if result.Name == toolName {
				return true
			}
		}
		if node.Message.Role == llm.RoleTool && strings.Contains(node.Message.Text, toolName) {
			return true
		}
	}
	return false
}
