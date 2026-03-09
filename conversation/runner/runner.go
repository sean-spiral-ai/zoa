package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	convdb "zoa/conversation/db"
	"zoa/llm"
	tools "zoa/tools"
)

var ErrRunInProgress = errors.New("a run is already in progress")

type RunStatus int

const (
	RunCompleted RunStatus = iota
	RunInterrupted
	RunErrored
)

type RunResult struct {
	Status    RunStatus
	FinalText string
	HeadHash  string
	Turns     int
	Err       error
}

type RunnerConfig struct {
	DB               *convdb.DB
	Ref              string
	Client           llm.Client
	Model            string
	Tools            []tools.Tool
	Temperature      float64
	SystemPrompt     string
	LeaseHolder      string
	LeaseDuration    time.Duration
	GracePeriod      time.Duration
	MaxTurns         int
	ResponseMimeType string
	ResponseSchema   map[string]any
	DisableTools     bool
}

type ConversationRunner struct {
	db               *convdb.DB
	ref              string
	client           llm.Client
	model            string
	registry         *tools.Registry
	temperature      float64
	systemPrompt     string
	leaseHolder      string
	leaseDuration    time.Duration
	gracePeriod      time.Duration
	maxTurns         int
	responseMimeType string
	responseSchema   map[string]any
	disableTools     bool

	mu               sync.Mutex
	head             string
	runCancel        context.CancelFunc
	activeToolCancel context.CancelFunc
	running          bool
	done             chan struct{}
	result           RunResult
	interruptCh      chan struct{}
	stopOnce         sync.Once
	leaseStop        chan struct{}
}

func NewRunner(cfg RunnerConfig) (*ConversationRunner, error) {
	if cfg.DB == nil {
		return nil, fmt.Errorf("db is required")
	}
	if strings.TrimSpace(cfg.Ref) == "" {
		return nil, fmt.Errorf("ref is required")
	}
	if cfg.Client == nil {
		return nil, fmt.Errorf("client is required")
	}
	if cfg.LeaseDuration <= 0 {
		cfg.LeaseDuration = time.Minute
	}
	if cfg.GracePeriod <= 0 {
		cfg.GracePeriod = 1500 * time.Millisecond
	}
	if cfg.MaxTurns <= 0 {
		cfg.MaxTurns = 20
	}
	if strings.TrimSpace(cfg.LeaseHolder) == "" {
		cfg.LeaseHolder = fmt.Sprintf("runner-%d", time.Now().UTC().UnixNano())
	}
	if err := cfg.DB.AcquireLease(cfg.Ref, cfg.LeaseHolder, cfg.LeaseDuration); err != nil {
		return nil, err
	}
	ref, err := cfg.DB.GetRef(cfg.Ref)
	if err != nil {
		_ = cfg.DB.ReleaseLease(cfg.Ref, cfg.LeaseHolder)
		return nil, err
	}
	return &ConversationRunner{
		db:               cfg.DB,
		ref:              cfg.Ref,
		client:           cfg.Client,
		model:            cfg.Model,
		registry:         tools.NewRegistry(cfg.Tools),
		temperature:      cfg.Temperature,
		systemPrompt:     strings.TrimSpace(cfg.SystemPrompt),
		leaseHolder:      cfg.LeaseHolder,
		leaseDuration:    cfg.LeaseDuration,
		gracePeriod:      cfg.GracePeriod,
		maxTurns:         cfg.MaxTurns,
		responseMimeType: strings.TrimSpace(cfg.ResponseMimeType),
		responseSchema:   cloneMap(cfg.ResponseSchema),
		disableTools:     cfg.DisableTools,
		head:             ref.Hash,
	}, nil
}

func (r *ConversationRunner) Run(ctx context.Context, userMessage string) error {
	if strings.TrimSpace(userMessage) == "" {
		return fmt.Errorf("user message is required")
	}
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return ErrRunInProgress
	}
	r.running = true
	r.done = make(chan struct{})
	r.interruptCh = make(chan struct{})
	r.stopOnce = sync.Once{}
	r.leaseStop = make(chan struct{})
	runCtx, cancel := context.WithCancel(ctx)
	r.runCancel = cancel
	r.mu.Unlock()

	if err := r.ensureSystemPrompt(); err != nil {
		r.finish(RunResult{Status: RunErrored, HeadHash: r.HeadHash(), Err: err})
		return nil
	}

	newHead, err := r.db.AdvanceRef(r.ref, r.HeadHash(), convdb.Message{
		Role: llm.RoleUser,
		Text: strings.TrimSpace(userMessage),
	}, r.leaseHolder)
	if err != nil {
		r.finish(RunResult{Status: RunErrored, HeadHash: r.HeadHash(), Err: err})
		return nil
	}
	r.setHead(newHead)

	go r.renewLeaseLoop()
	go func() {
		result := r.runLoop(runCtx)
		r.finish(result)
	}()
	return nil
}

func (r *ConversationRunner) Wait() RunResult {
	r.mu.Lock()
	done := r.done
	result := r.result
	running := r.running
	r.mu.Unlock()
	if !running || done == nil {
		return result
	}
	<-done
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.result
}

func (r *ConversationRunner) Stop() {
	r.mu.Lock()
	done := r.done
	cancel := r.runCancel
	activeCancel := r.activeToolCancel
	grace := r.gracePeriod
	r.mu.Unlock()
	if done == nil {
		_ = r.db.ReleaseLease(r.ref, r.leaseHolder)
		return
	}

	r.stopOnce.Do(func() {
		close(r.interruptCh)
	})

	select {
	case <-done:
	case <-time.After(grace):
		if cancel != nil {
			cancel()
		}
		if activeCancel != nil {
			activeCancel()
		}
		<-done
	}
	_ = r.db.ReleaseLease(r.ref, r.leaseHolder)
}

func (r *ConversationRunner) Release() error {
	r.mu.Lock()
	running := r.running
	r.mu.Unlock()
	if running {
		return ErrRunInProgress
	}
	return r.db.ReleaseLease(r.ref, r.leaseHolder)
}

func (r *ConversationRunner) HeadHash() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.head
}

func (r *ConversationRunner) Ref() string {
	return r.ref
}

func (r *ConversationRunner) ensureSystemPrompt() error {
	if r.systemPrompt == "" {
		return nil
	}
	if len(mustLoadChain(r.db, r.HeadHash())) > 0 {
		return nil
	}
	newHead, err := r.db.AdvanceRef(r.ref, r.HeadHash(), convdb.Message{
		Role: llm.RoleSystem,
		Text: r.systemPrompt,
	}, r.leaseHolder)
	if err != nil {
		return err
	}
	r.setHead(newHead)
	return nil
}

func (r *ConversationRunner) runLoop(ctx context.Context) RunResult {
	for turn := 1; turn <= r.maxTurns; turn++ {
		if r.interrupted() {
			return RunResult{Status: RunInterrupted, HeadHash: r.HeadHash(), Turns: turn - 1}
		}
		chain, err := r.db.LoadChain(r.HeadHash())
		if err != nil {
			return RunResult{Status: RunErrored, HeadHash: r.HeadHash(), Turns: turn - 1, Err: err}
		}

		toolSpecs := r.registry.Specs()
		if r.disableTools {
			toolSpecs = nil
		}
		resp, err := r.client.Complete(ctx, llm.CompletionRequest{
			Model:            r.model,
			Messages:         chainMessages(chain),
			Tools:            toolSpecs,
			Temperature:      r.temperature,
			ResponseMimeType: r.responseMimeType,
			ResponseSchema:   cloneMap(r.responseSchema),
		})
		if err != nil {
			if r.interrupted() || errors.Is(ctx.Err(), context.Canceled) {
				return RunResult{Status: RunInterrupted, HeadHash: r.HeadHash(), Turns: turn - 1}
			}
			return RunResult{Status: RunErrored, HeadHash: r.HeadHash(), Turns: turn - 1, Err: err}
		}

		assistantMsg := convdb.Message{
			Role:      llm.RoleAssistant,
			Text:      resp.Text,
			Parts:     cloneAssistantParts(resp.Parts),
			ToolCalls: cloneToolCalls(resp.ToolCalls),
		}
		newHead, err := r.db.AdvanceRef(r.ref, r.HeadHash(), assistantMsg, r.leaseHolder)
		if err != nil {
			return RunResult{Status: RunErrored, HeadHash: r.HeadHash(), Turns: turn - 1, Err: err}
		}
		r.setHead(newHead)

		if r.interrupted() {
			return RunResult{Status: RunInterrupted, HeadHash: r.HeadHash(), Turns: turn}
		}
		if len(resp.ToolCalls) == 0 {
			return RunResult{Status: RunCompleted, FinalText: strings.TrimSpace(resp.Text), HeadHash: r.HeadHash(), Turns: turn}
		}

		results := make([]llm.ToolResult, len(resp.ToolCalls))
		for i, call := range resp.ToolCalls {
			if r.interrupted() {
				fillInterruptedResults(results, resp.ToolCalls, i)
				break
			}
			tool, ok := r.registry.Get(call.Name)
			if !ok {
				results[i] = interruptedOrErrorToolResult(call, fmt.Sprintf("unknown tool: %s", call.Name), false)
				continue
			}
			toolCtx, toolCancel := context.WithCancel(ctx)
			r.setActiveToolCancel(toolCancel)
			output, execErr := tool.Execute(toolCtx, call.Args)
			r.setActiveToolCancel(nil)
			toolCancel()

			if r.interrupted() {
				fillInterruptedResults(results, resp.ToolCalls, i)
				break
			}
			if execErr != nil {
				output = execErr.Error()
			}
			results[i] = llm.ToolResult{
				CallID:  call.ID,
				Name:    call.Name,
				Output:  output,
				IsError: execErr != nil,
			}
		}

		newHead, err = r.db.AdvanceRef(r.ref, r.HeadHash(), convdb.Message{
			Role:        llm.RoleTool,
			ToolResults: results,
		}, r.leaseHolder)
		if err != nil {
			return RunResult{Status: RunErrored, HeadHash: r.HeadHash(), Turns: turn, Err: err}
		}
		r.setHead(newHead)
		if r.interrupted() {
			return RunResult{Status: RunInterrupted, HeadHash: r.HeadHash(), Turns: turn}
		}
	}
	return RunResult{Status: RunErrored, HeadHash: r.HeadHash(), Turns: r.maxTurns, Err: fmt.Errorf("max turns reached")}
}

func (r *ConversationRunner) interrupted() bool {
	select {
	case <-r.interruptCh:
		return true
	default:
		return false
	}
}

func (r *ConversationRunner) finish(result RunResult) {
	r.mu.Lock()
	r.result = result
	r.running = false
	done := r.done
	cancel := r.runCancel
	leaseStop := r.leaseStop
	r.runCancel = nil
	r.activeToolCancel = nil
	r.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if leaseStop != nil {
		close(leaseStop)
	}
	_ = r.db.ReleaseLease(r.ref, r.leaseHolder)
	if done != nil {
		close(done)
	}
}

func (r *ConversationRunner) renewLeaseLoop() {
	interval := r.leaseDuration / 2
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-r.leaseStop:
			return
		case <-ticker.C:
			_ = r.db.RenewLease(r.ref, r.leaseHolder, r.leaseDuration)
		}
	}
}

func (r *ConversationRunner) setHead(hash string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.head = hash
}

func (r *ConversationRunner) setActiveToolCancel(cancel context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.activeToolCancel = cancel
}

func chainMessages(chain []convdb.Node) []llm.Message {
	out := make([]llm.Message, 0, len(chain))
	for _, node := range chain {
		out = append(out, node.Message)
	}
	return out
}

func fillInterruptedResults(results []llm.ToolResult, calls []llm.ToolCall, start int) {
	for i := start; i < len(calls); i++ {
		results[i] = interruptedOrErrorToolResult(calls[i], "tool execution interrupted", true)
	}
}

func interruptedOrErrorToolResult(call llm.ToolCall, output string, interrupted bool) llm.ToolResult {
	return llm.ToolResult{
		CallID:  call.ID,
		Name:    call.Name,
		Output:  output,
		IsError: true,
	}
}

func cloneAssistantParts(in []llm.AssistantPart) []llm.AssistantPart {
	out := make([]llm.AssistantPart, 0, len(in))
	for _, p := range in {
		part := llm.AssistantPart{
			Text:             p.Text,
			ThoughtSignature: p.ThoughtSignature,
		}
		if p.ToolCall != nil {
			call := *p.ToolCall
			call.Args = cloneMap(call.Args)
			part.ToolCall = &call
		}
		out = append(out, part)
	}
	return out
}

func cloneToolCalls(in []llm.ToolCall) []llm.ToolCall {
	out := make([]llm.ToolCall, 0, len(in))
	for _, call := range in {
		out = append(out, llm.ToolCall{
			ID:               call.ID,
			Name:             call.Name,
			Args:             cloneMap(call.Args),
			ThoughtSignature: call.ThoughtSignature,
		})
	}
	return out
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = cloneAny(v)
	}
	return out
}

func cloneAny(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return cloneMap(t)
	case []any:
		out := make([]any, len(t))
		for i := range t {
			out[i] = cloneAny(t[i])
		}
		return out
	default:
		return v
	}
}

func mustLoadChain(db *convdb.DB, head string) []convdb.Node {
	chain, err := db.LoadChain(head)
	if err != nil {
		return nil
	}
	return chain
}
