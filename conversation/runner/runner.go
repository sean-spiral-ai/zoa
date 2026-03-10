package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	convdb "zoa/conversation/db"
	"zoa/internal/agentloop/llm"
	tools "zoa/internal/agentloop/tools"
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
	Ref          *convdb.LeasedRef
	Client       llm.Client
	Model        string
	Tools        []tools.Tool
	Temperature  float64
	SystemPrompt string
	GracePeriod  time.Duration
	MaxTurns     int
}

type RunOptions struct {
	ResponseMimeType string
	ResponseSchema   map[string]any
}

type ConversationRunner struct {
	ref          *convdb.LeasedRef
	client       llm.Client
	model        string
	registry     *tools.Registry
	temperature  float64
	systemPrompt string
	gracePeriod  time.Duration
	maxTurns     int

	mu               sync.Mutex
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
	if cfg.Ref == nil {
		return nil, fmt.Errorf("ref is required")
	}
	if cfg.Client == nil {
		return nil, fmt.Errorf("client is required")
	}
	if cfg.GracePeriod <= 0 {
		cfg.GracePeriod = 1500 * time.Millisecond
	}
	if cfg.MaxTurns <= 0 {
		cfg.MaxTurns = 20
	}
	return &ConversationRunner{
		ref:          cfg.Ref,
		client:       cfg.Client,
		model:        cfg.Model,
		registry:     tools.NewRegistry(cfg.Tools),
		temperature:  cfg.Temperature,
		systemPrompt: strings.TrimSpace(cfg.SystemPrompt),
		gracePeriod:  cfg.GracePeriod,
		maxTurns:     cfg.MaxTurns,
	}, nil
}

func (r *ConversationRunner) Run(ctx context.Context, userMessage string, opts RunOptions) error {
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

	newHead, err := r.ref.Append(convdb.Message{
		Role: llm.RoleUser,
		Text: strings.TrimSpace(userMessage),
	})
	if err != nil {
		r.finish(RunResult{Status: RunErrored, HeadHash: r.HeadHash(), Err: err})
		return nil
	}
	_ = newHead

	go r.renewLeaseLoop()
	go func() {
		result := r.runLoop(runCtx, opts)
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
		_ = r.ref.Close()
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
	_ = r.ref.Close()
}

func (r *ConversationRunner) Release() error {
	r.mu.Lock()
	running := r.running
	r.mu.Unlock()
	if running {
		return ErrRunInProgress
	}
	return r.ref.Close()
}

func (r *ConversationRunner) HeadHash() string {
	return r.ref.Hash()
}

func (r *ConversationRunner) Ref() string {
	return r.ref.Name()
}

func (r *ConversationRunner) ensureSystemPrompt() error {
	if r.systemPrompt == "" {
		return nil
	}
	if len(mustLoadChain(r.ref)) > 0 {
		return nil
	}
	_, err := r.ref.Append(convdb.Message{
		Role: llm.RoleSystem,
		Text: r.systemPrompt,
	})
	return err
}

func (r *ConversationRunner) runLoop(ctx context.Context, opts RunOptions) RunResult {
	for turn := 1; turn <= r.maxTurns; turn++ {
		if r.interrupted() {
			return RunResult{Status: RunInterrupted, HeadHash: r.HeadHash(), Turns: turn - 1}
		}
		chain, err := r.ref.LoadChain()
		if err != nil {
			return RunResult{Status: RunErrored, HeadHash: r.HeadHash(), Turns: turn - 1, Err: err}
		}

		resp, err := r.client.Complete(ctx, llm.CompletionRequest{
			Model:            r.model,
			Messages:         chainMessages(chain),
			Tools:            r.registry.Specs(),
			Temperature:      r.temperature,
			ResponseMimeType: strings.TrimSpace(opts.ResponseMimeType),
			ResponseSchema:   cloneMap(opts.ResponseSchema),
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
		if _, err := r.ref.Append(assistantMsg); err != nil {
			return RunResult{Status: RunErrored, HeadHash: r.HeadHash(), Turns: turn - 1, Err: err}
		}

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

		if _, err := r.ref.Append(convdb.Message{
			Role:        llm.RoleTool,
			ToolResults: results,
		}); err != nil {
			return RunResult{Status: RunErrored, HeadHash: r.HeadHash(), Turns: turn, Err: err}
		}
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
	_ = r.ref.Close()
	if done != nil {
		close(done)
	}
}

func (r *ConversationRunner) renewLeaseLoop() {
	interval := time.Until(r.ref.LeaseDeadline()) / 2
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
			_ = r.ref.Renew()
			nextInterval := time.Until(r.ref.LeaseDeadline()) / 2
			if nextInterval <= 0 {
				nextInterval = time.Second
			}
			ticker.Reset(nextInterval)
		}
	}
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

func mustLoadChain(ref *convdb.LeasedRef) []convdb.Node {
	chain, err := ref.LoadChain()
	if err != nil {
		return nil
	}
	return chain
}
