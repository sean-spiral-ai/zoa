package conversation

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

type ExecutorConfig struct {
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

type Executor struct {
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

func NewExecutor(cfg ExecutorConfig) (*Executor, error) {
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
	return &Executor{
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

func (e *Executor) Run(ctx context.Context, userMessage string, opts RunOptions) error {
	if strings.TrimSpace(userMessage) == "" {
		return fmt.Errorf("user message is required")
	}
	e.mu.Lock()
	if e.running {
		e.mu.Unlock()
		return ErrRunInProgress
	}
	e.running = true
	e.done = make(chan struct{})
	e.interruptCh = make(chan struct{})
	e.stopOnce = sync.Once{}
	e.leaseStop = make(chan struct{})
	runCtx, cancel := context.WithCancel(ctx)
	e.runCancel = cancel
	e.mu.Unlock()

	if err := e.ensureSystemPrompt(); err != nil {
		e.finish(RunResult{Status: RunErrored, HeadHash: e.HeadHash(), Err: err})
		return nil
	}

	if _, err := e.ref.Append(convdb.Message{
		Role: llm.RoleUser,
		Text: strings.TrimSpace(userMessage),
	}); err != nil {
		e.finish(RunResult{Status: RunErrored, HeadHash: e.HeadHash(), Err: err})
		return nil
	}

	go e.renewLeaseLoop()
	go func() {
		result := e.runLoop(runCtx, opts)
		e.finish(result)
	}()
	return nil
}

func (e *Executor) Wait() RunResult {
	e.mu.Lock()
	done := e.done
	result := e.result
	running := e.running
	e.mu.Unlock()
	if !running || done == nil {
		return result
	}
	<-done
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.result
}

func (e *Executor) Stop() {
	e.mu.Lock()
	done := e.done
	cancel := e.runCancel
	activeCancel := e.activeToolCancel
	grace := e.gracePeriod
	e.mu.Unlock()
	if done == nil {
		_ = e.ref.Close()
		return
	}

	e.stopOnce.Do(func() {
		close(e.interruptCh)
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
	_ = e.ref.Close()
}

func (e *Executor) Release() error {
	e.mu.Lock()
	running := e.running
	e.mu.Unlock()
	if running {
		return ErrRunInProgress
	}
	return e.ref.Close()
}

func (e *Executor) HeadHash() string {
	return e.ref.Hash()
}

func (e *Executor) Ref() string {
	return e.ref.Name()
}

func (e *Executor) ensureSystemPrompt() error {
	if e.systemPrompt == "" {
		return nil
	}
	if len(mustLoadChain(e.ref)) > 0 {
		return nil
	}
	_, err := e.ref.Append(convdb.Message{
		Role: llm.RoleSystem,
		Text: e.systemPrompt,
	})
	return err
}

func (e *Executor) runLoop(ctx context.Context, opts RunOptions) RunResult {
	for turn := 1; turn <= e.maxTurns; turn++ {
		if e.interrupted() {
			return RunResult{Status: RunInterrupted, HeadHash: e.HeadHash(), Turns: turn - 1}
		}
		chain, err := e.ref.LoadChain()
		if err != nil {
			return RunResult{Status: RunErrored, HeadHash: e.HeadHash(), Turns: turn - 1, Err: err}
		}

		resp, err := e.client.Complete(ctx, llm.CompletionRequest{
			Model:            e.model,
			Messages:         chainMessages(chain),
			Tools:            e.registry.Specs(),
			Temperature:      e.temperature,
			ResponseMimeType: strings.TrimSpace(opts.ResponseMimeType),
			ResponseSchema:   cloneMap(opts.ResponseSchema),
		})
		if err != nil {
			if e.interrupted() || errors.Is(ctx.Err(), context.Canceled) {
				return RunResult{Status: RunInterrupted, HeadHash: e.HeadHash(), Turns: turn - 1}
			}
			return RunResult{Status: RunErrored, HeadHash: e.HeadHash(), Turns: turn - 1, Err: err}
		}

		assistantMsg := convdb.Message{
			Role:      llm.RoleAssistant,
			Text:      resp.Text,
			Parts:     cloneAssistantParts(resp.Parts),
			ToolCalls: cloneToolCalls(resp.ToolCalls),
		}
		if _, err := e.ref.Append(assistantMsg); err != nil {
			return RunResult{Status: RunErrored, HeadHash: e.HeadHash(), Turns: turn - 1, Err: err}
		}

		if e.interrupted() {
			return RunResult{Status: RunInterrupted, HeadHash: e.HeadHash(), Turns: turn}
		}
		if len(resp.ToolCalls) == 0 {
			return RunResult{Status: RunCompleted, FinalText: strings.TrimSpace(resp.Text), HeadHash: e.HeadHash(), Turns: turn}
		}

		results := make([]llm.ToolResult, len(resp.ToolCalls))
		for i, call := range resp.ToolCalls {
			if e.interrupted() {
				fillInterruptedResults(results, resp.ToolCalls, i)
				break
			}
			tool, ok := e.registry.Get(call.Name)
			if !ok {
				results[i] = interruptedOrErrorToolResult(call, fmt.Sprintf("unknown tool: %s", call.Name), false)
				continue
			}
			toolCtx, toolCancel := context.WithCancel(ctx)
			e.setActiveToolCancel(toolCancel)
			output, execErr := tool.Execute(toolCtx, call.Args)
			e.setActiveToolCancel(nil)
			toolCancel()

			if e.interrupted() {
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

		if _, err := e.ref.Append(convdb.Message{
			Role:        llm.RoleTool,
			ToolResults: results,
		}); err != nil {
			return RunResult{Status: RunErrored, HeadHash: e.HeadHash(), Turns: turn, Err: err}
		}
		if e.interrupted() {
			return RunResult{Status: RunInterrupted, HeadHash: e.HeadHash(), Turns: turn}
		}
	}
	return RunResult{Status: RunErrored, HeadHash: e.HeadHash(), Turns: e.maxTurns, Err: fmt.Errorf("max turns reached")}
}

func (e *Executor) interrupted() bool {
	select {
	case <-e.interruptCh:
		return true
	default:
		return false
	}
}

func (e *Executor) finish(result RunResult) {
	e.mu.Lock()
	e.result = result
	e.running = false
	done := e.done
	cancel := e.runCancel
	leaseStop := e.leaseStop
	e.runCancel = nil
	e.activeToolCancel = nil
	e.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if leaseStop != nil {
		close(leaseStop)
	}
	_ = e.ref.Close()
	if done != nil {
		close(done)
	}
}

func (e *Executor) renewLeaseLoop() {
	interval := time.Until(e.ref.LeaseDeadline()) / 2
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-e.leaseStop:
			return
		case <-ticker.C:
			_ = e.ref.Renew()
			nextInterval := time.Until(e.ref.LeaseDeadline()) / 2
			if nextInterval <= 0 {
				nextInterval = time.Second
			}
			ticker.Reset(nextInterval)
		}
	}
}

func (e *Executor) setActiveToolCancel(cancel context.CancelFunc) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.activeToolCancel = cancel
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
	switch tv := v.(type) {
	case map[string]any:
		return cloneMap(tv)
	case []any:
		out := make([]any, len(tv))
		for i, item := range tv {
			out[i] = cloneAny(item)
		}
		return out
	default:
		return tv
	}
}

func mustLoadChain(ref *convdb.LeasedRef) []convdb.Node {
	chain, err := ref.LoadChain()
	if err != nil {
		return nil
	}
	return chain
}
