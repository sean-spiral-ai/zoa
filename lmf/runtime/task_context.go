package lmf

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	baselineagent "zoa/baselineagent"
)

type TaskContextOptions struct {
	APIKey      string
	CWD         string
	Model       string
	MaxTurns    int
	Timeout     time.Duration
	Temperature float64
}

type TaskContext struct {
	ctx        context.Context
	apiKey     string
	baseConfig baselineagent.ConversationConfig
	mainConv   baselineagent.Conversation
}

func NewTaskContext(ctx context.Context, opts TaskContextOptions) (*TaskContext, error) {
	cwd := strings.TrimSpace(opts.CWD)
	if cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("resolve cwd: %w", err)
		}
		cwd = wd
	}
	absCWD, err := filepath.Abs(cwd)
	if err != nil {
		return nil, fmt.Errorf("resolve absolute cwd: %w", err)
	}

	apiKey, _ := baselineagent.ResolveAPIKey(opts.APIKey)

	toolset, err := baselineagent.NewBuiltinCodingTools(absCWD)
	if err != nil {
		return nil, fmt.Errorf("initialize baseline tools: %w", err)
	}

	baseConfig := baselineagent.ConversationConfig{
		CWD:         absCWD,
		Model:       strings.TrimSpace(opts.Model),
		MaxTurns:    opts.MaxTurns,
		Timeout:     opts.Timeout,
		Temperature: opts.Temperature,
		Tools:       toolset,
	}

	return &TaskContext{
		ctx:        ctx,
		apiKey:     apiKey,
		baseConfig: baseConfig,
		mainConv:   nil,
	}, nil
}

func (t *TaskContext) Context() context.Context {
	return t.ctx
}

// NLExec appends to the TaskContext's persistent conversation and returns raw text.
func (t *TaskContext) NLExec(prompt string, data map[string]any) (string, error) {
	if err := t.ensureMainConversation(); err != nil {
		return "", err
	}
	instruction, err := nlExecInstruction(prompt, data)
	if err != nil {
		return "", err
	}
	res, err := t.mainConv.Prompt(t.ctx, instruction)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(res.FinalResponse), nil
}

// NLExecTyped appends to the TaskContext's persistent conversation and decodes a JSON response into out.
func (t *TaskContext) NLExecTyped(prompt string, data map[string]any, out any) error {
	if err := t.ensureMainConversation(); err != nil {
		return err
	}
	instruction, err := nlExecTypedInstruction(prompt, data)
	if err != nil {
		return err
	}
	schema, err := baselineagent.JSONSchemaForOutputValue(out)
	if err != nil {
		return err
	}
	res, err := t.mainConv.PromptStructured(t.ctx, instruction, baselineagent.JSONSchemaFormat{
		SchemaObject: schema,
	})
	if err != nil {
		return err
	}

	if err := json.Unmarshal([]byte(strings.TrimSpace(res.FinalResponse)), out); err != nil {
		return fmt.Errorf("decode typed NLExec response: %w; raw response: %s", err, strings.TrimSpace(res.FinalResponse))
	}
	return nil
}

// NLExecTyped is a generic helper for typed NLExec return values.
func NLExecTyped[T any](tc *TaskContext, prompt string, data map[string]any) (T, error) {
	var out T
	err := tc.NLExecTyped(prompt, data, &out)
	return out, err
}

// NLCondition evaluates a natural-language condition in an isolated fork of the main conversation.
func (t *TaskContext) NLCondition(conditionID string, conditionPrompt string, data map[string]any) error {
	if err := t.ensureMainConversation(); err != nil {
		return err
	}
	fork := t.mainConv.Fork()
	instruction, err := nlConditionInstruction(conditionID, conditionPrompt, data)
	if err != nil {
		return err
	}
	res, err := fork.PromptStructured(t.ctx, instruction, baselineagent.JSONSchemaFormat{
		SchemaObject: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"passed":      map[string]any{"type": "boolean"},
				"explanation": map[string]any{"type": "string"},
			},
			"required": []string{"passed", "explanation"},
		},
	})
	if err != nil {
		return err
	}

	parsed, err := parseConditionResultJSON(res.FinalResponse)
	if err != nil {
		return fmt.Errorf("parse NL condition response: %w; raw response: %s", err, strings.TrimSpace(res.FinalResponse))
	}
	if parsed.Passed {
		return nil
	}
	return &NLConditionError{
		ConditionID: conditionID,
		Prompt:      conditionPrompt,
		Context:     cloneContextMap(data),
		Explanation: strings.TrimSpace(parsed.Explanation),
	}
}

func (t *TaskContext) ensureMainConversation() error {
	if t.mainConv != nil {
		return nil
	}
	apiKey, err := t.resolveAPIKey()
	if err != nil {
		return err
	}
	conv, err := baselineagent.NewConversation(apiKey, t.baseConfig)
	if err != nil {
		return err
	}
	t.mainConv = conv
	return nil
}

func (t *TaskContext) resolveAPIKey() (string, error) {
	key, ok := baselineagent.ResolveAPIKey(t.apiKey)
	if !ok {
		return "", fmt.Errorf("%s is required for baselineagent backed operations", baselineagent.GeminiAPIKeyEnvVar)
	}
	t.apiKey = key
	return key, nil
}

func (t *TaskContext) conversationHistory() []baselineagent.ConversationMessage {
	if t.mainConv == nil {
		return []baselineagent.ConversationMessage{}
	}
	history := t.mainConv.History()
	if history == nil {
		return []baselineagent.ConversationMessage{}
	}
	return history
}

type NLConditionError struct {
	ConditionID string         `json:"condition_id"`
	Prompt      string         `json:"prompt"`
	Context     map[string]any `json:"context"`
	Explanation string         `json:"explanation"`
}

func (e *NLConditionError) Error() string {
	ctxJSON, _ := json.Marshal(e.Context)
	return fmt.Sprintf("nl condition failed [%s]: %s | prompt=%q | context=%s", e.ConditionID, e.Explanation, e.Prompt, string(ctxJSON))
}

func nlExecInstruction(prompt string, data map[string]any) (string, error) {
	if data == nil {
		return fmt.Sprintf(`
You are executing an LMFunction NLExec call.

Task:
%s

Return only the final answer text. Do not include markdown fences.
`, strings.TrimSpace(prompt)), nil
	}

	payload, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal NLExec payload: %w", err)
	}
	return fmt.Sprintf(`
You are executing an LMFunction NLExec call.

Task:
%s

Context JSON:
%s

Return only the final answer text. Do not include markdown fences.
`, strings.TrimSpace(prompt), string(payload)), nil
}

func nlExecTypedInstruction(prompt string, data map[string]any) (string, error) {
	if data == nil {
		return fmt.Sprintf(`
You are executing an LMFunction typed NLExec call.

Task:
%s

Return ONLY valid JSON representing your final answer.
No markdown fences. No extra prose.
`, strings.TrimSpace(prompt)), nil
	}

	payload, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal NLExec payload: %w", err)
	}
	return fmt.Sprintf(`
You are executing an LMFunction typed NLExec call.

Task:
%s

Context JSON:
%s

Return ONLY valid JSON representing your final answer.
No markdown fences. No extra prose.
`, strings.TrimSpace(prompt), string(payload)), nil
}

func nlConditionInstruction(conditionID string, conditionPrompt string, data map[string]any) (string, error) {
	payload, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal condition payload: %w", err)
	}
	return fmt.Sprintf(`
You are evaluating an LMFunction natural-language condition in isolation.

Condition ID:
%s

Condition to evaluate:
%s

Condition context JSON:
%s

Return ONLY a JSON object with this exact shape:
{"passed": <true|false>, "explanation": "short reason"}
`, conditionID, strings.TrimSpace(conditionPrompt), string(payload)), nil
}

type conditionJSON struct {
	Passed      bool   `json:"passed"`
	Explanation string `json:"explanation"`
}

func parseConditionResultJSON(text string) (conditionJSON, error) {
	var out conditionJSON
	if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &out); err != nil {
		return conditionJSON{}, err
	}
	out.Explanation = strings.TrimSpace(out.Explanation)
	return out, nil
}

func cloneContextMap(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	b, err := json.Marshal(in)
	if err != nil {
		return map[string]any{"_unserializable": true}
	}
	out := map[string]any{}
	if err := json.Unmarshal(b, &out); err != nil {
		return map[string]any{"_unserializable": true}
	}
	return out
}
