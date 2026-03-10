package conversation

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	convdb "zoa/conversation/db"
	"zoa/internal/agentloop/llm"
)

func TestStartServerServesTraceEndpoints(t *testing.T) {
	db, err := convdb.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()

	systemHash, err := db.Append(convdb.RootHash, convdb.Message{Role: llm.RoleSystem, Text: "sys"})
	if err != nil {
		t.Fatalf("append system: %v", err)
	}
	if _, err := db.Append(systemHash, convdb.Message{Role: llm.RoleUser, Text: "hello"}); err != nil {
		t.Fatalf("append user: %v", err)
	}

	srv, ln, err := StartServer("127.0.0.1:0", db)
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer func() { _ = srv.Close() }()

	client := &http.Client{Timeout: 2 * time.Second}

	resp, err := client.Get("http://" + ln.Addr().String() + "/api/tree")
	if err != nil {
		t.Fatalf("get tree: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tree status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var tree []convdb.TraceNode
	if err := json.NewDecoder(resp.Body).Decode(&tree); err != nil {
		t.Fatalf("decode tree: %v", err)
	}
	if len(tree) != 3 {
		t.Fatalf("tree node count = %d, want 3", len(tree))
	}
	if tree[0].Role != "root" {
		t.Fatalf("first role = %q, want root", tree[0].Role)
	}
	if tree[1].Summary != "sys" {
		t.Fatalf("system summary = %q, want sys", tree[1].Summary)
	}

	resp, err = client.Get("http://" + ln.Addr().String() + "/api/nodes?since_id=1")
	if err != nil {
		t.Fatalf("get since: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("since status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var since []convdb.TraceNode
	if err := json.NewDecoder(resp.Body).Decode(&since); err != nil {
		t.Fatalf("decode since: %v", err)
	}
	if len(since) != 2 {
		t.Fatalf("since node count = %d, want 2", len(since))
	}
	if since[0].Role != string(llm.RoleSystem) || since[1].Role != string(llm.RoleUser) {
		t.Fatalf("unexpected roles from since endpoint: %#v", since)
	}
}
