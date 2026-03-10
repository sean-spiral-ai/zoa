package runner

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	convdb "zoa/conversation/db"
	"zoa/internal/agentloop/llm"
	tools "zoa/internal/agentloop/tools"
)

func TestRunnerCompletesWithoutTools(t *testing.T) {
	db := openRunnerDB(t)
	mustCreateRef(t, db, "sessions/default")

	client := &stubClient{
		complete: func(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
			if len(req.Messages) != 2 {
				t.Fatalf("message count = %d, want 2", len(req.Messages))
			}
			return llm.CompletionResponse{Text: "done"}, nil
		},
	}

	r, err := NewRunner(RunnerConfig{
		DB:           db,
		Ref:          "sessions/default",
		Client:       client,
		Model:        "test-model",
		SystemPrompt: "sys",
		LeaseHolder:  "runner-1",
	})
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}
	if err := r.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("run: %v", err)
	}
	result := r.Wait()
	if result.Status != RunCompleted || result.FinalText != "done" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestRunnerCompletesToolLoop(t *testing.T) {
	db := openRunnerDB(t)
	mustCreateRef(t, db, "sessions/default")

	client := &stubClient{}
	client.complete = func(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
		switch len(req.Messages) {
		case 2:
			return llm.CompletionResponse{
				ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "echo", Args: map[string]any{"x": "y"}}},
			}, nil
		case 4:
			if len(req.Messages[3].ToolResults) != 1 || req.Messages[3].ToolResults[0].Output != "ok" {
				t.Fatalf("unexpected tool result message: %+v", req.Messages[3])
			}
			return llm.CompletionResponse{Text: "final"}, nil
		default:
			t.Fatalf("unexpected message count: %d", len(req.Messages))
			return llm.CompletionResponse{}, nil
		}
	}

	r, err := NewRunner(RunnerConfig{
		DB:           db,
		Ref:          "sessions/default",
		Client:       client,
		Model:        "test-model",
		SystemPrompt: "sys",
		Tools:        []tools.Tool{echoTool{}},
		LeaseHolder:  "runner-1",
	})
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}
	if err := r.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("run: %v", err)
	}
	result := r.Wait()
	if result.Status != RunCompleted || result.FinalText != "final" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestRunnerInterruptsMidToolCall(t *testing.T) {
	db := openRunnerDB(t)
	mustCreateRef(t, db, "sessions/default")

	toolStarted := make(chan struct{})
	toolRelease := make(chan struct{})
	client := &stubClient{
		complete: func(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
			return llm.CompletionResponse{
				ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "block"}},
			}, nil
		},
	}
	r, err := NewRunner(RunnerConfig{
		DB:           db,
		Ref:          "sessions/default",
		Client:       client,
		Model:        "test-model",
		SystemPrompt: "sys",
		Tools: []tools.Tool{blockingTool{
			started: toolStarted,
			release: toolRelease,
		}},
		LeaseHolder:   "runner-1",
		GracePeriod:   10 * time.Millisecond,
		LeaseDuration: time.Second,
	})
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}
	if err := r.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("run: %v", err)
	}
	<-toolStarted
	r.Stop()
	close(toolRelease)
	result := r.Wait()
	if result.Status != RunInterrupted {
		t.Fatalf("result status = %v, want interrupted", result.Status)
	}

	chain, err := db.LoadChain(r.HeadHash())
	if err != nil {
		t.Fatalf("load chain: %v", err)
	}
	last := chain[len(chain)-1].Message
	if len(last.ToolResults) != 1 || last.ToolResults[0].Output != "tool execution interrupted" {
		t.Fatalf("unexpected final tool results: %+v", last.ToolResults)
	}
}

func TestRunnerRejectsConcurrentRun(t *testing.T) {
	db := openRunnerDB(t)
	mustCreateRef(t, db, "sessions/default")
	wait := make(chan struct{})
	client := &stubClient{
		complete: func(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
			<-wait
			return llm.CompletionResponse{Text: "done"}, nil
		},
	}
	r, err := NewRunner(RunnerConfig{DB: db, Ref: "sessions/default", Client: client, Model: "test-model", LeaseHolder: "runner-1"})
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}
	if err := r.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if err := r.Run(context.Background(), "again"); !errors.Is(err, ErrRunInProgress) {
		t.Fatalf("second run error = %v, want %v", err, ErrRunInProgress)
	}
	close(wait)
	_ = r.Wait()
}

func TestRunnerFailsIfRefMoved(t *testing.T) {
	db := openRunnerDB(t)
	mustCreateRef(t, db, "sessions/default")
	client := &stubClient{
		complete: func(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
			mustAppendExternal(t, db, "sessions/default")
			return llm.CompletionResponse{Text: "done"}, nil
		},
	}
	r, err := NewRunner(RunnerConfig{DB: db, Ref: "sessions/default", Client: client, Model: "test-model", LeaseHolder: "runner-1"})
	if err != nil {
		t.Fatalf("new runner: %v", err)
	}
	if err := r.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("run: %v", err)
	}
	result := r.Wait()
	if !errors.Is(result.Err, convdb.ErrRefMoved) {
		t.Fatalf("result error = %v, want %v", result.Err, convdb.ErrRefMoved)
	}
}

func TestRunnerReleaseAllowsNewLease(t *testing.T) {
	db := openRunnerDB(t)
	mustCreateRef(t, db, "sessions/default")
	r1, err := NewRunner(RunnerConfig{DB: db, Ref: "sessions/default", Client: &stubClient{}, Model: "test-model", LeaseHolder: "runner-1"})
	if err != nil {
		t.Fatalf("new runner 1: %v", err)
	}
	if err := r1.Release(); err != nil {
		t.Fatalf("release runner 1: %v", err)
	}
	if _, err := NewRunner(RunnerConfig{DB: db, Ref: "sessions/default", Client: &stubClient{}, Model: "test-model", LeaseHolder: "runner-2"}); err != nil {
		t.Fatalf("new runner 2: %v", err)
	}
}

type stubClient struct {
	mu       sync.Mutex
	complete func(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error)
}

func (c *stubClient) Complete(ctx context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	c.mu.Lock()
	fn := c.complete
	c.mu.Unlock()
	if fn == nil {
		return llm.CompletionResponse{Text: "done"}, nil
	}
	return fn(ctx, req)
}

type echoTool struct{}

func (echoTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{Name: "echo", Schema: map[string]any{"type": "object"}}
}

func (echoTool) Execute(_ context.Context, _ map[string]any) (string, error) {
	return "ok", nil
}

type blockingTool struct {
	started chan struct{}
	release chan struct{}
}

func (t blockingTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{Name: "block", Schema: map[string]any{"type": "object"}}
}

func (t blockingTool) Execute(ctx context.Context, _ map[string]any) (string, error) {
	close(t.started)
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-t.release:
		return "released", nil
	}
}

func openRunnerDB(t *testing.T) *convdb.DB {
	t.Helper()
	db, err := convdb.Open(filepath.Join(t.TempDir(), "conversation.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func mustCreateRef(t *testing.T, db *convdb.DB, name string) {
	t.Helper()
	if err := db.CreateRef(name, convdb.RootHash); err != nil {
		t.Fatalf("create ref: %v", err)
	}
}

func mustAppendExternal(t *testing.T, db *convdb.DB, name string) {
	t.Helper()
	headRef, err := db.GetRef(name)
	if err != nil {
		t.Fatalf("get ref: %v", err)
	}
	hash, err := db.Append(headRef.Hash, convdb.Message{Role: llm.RoleUser, Text: "external"})
	if err != nil {
		t.Fatalf("append external: %v", err)
	}
	if err := db.SetRef(name, hash); err != nil {
		t.Fatalf("set ref external: %v", err)
	}
}
