package lmf

import baselineagent "zoa/baselineagent"

type ExecFunc func(tc *TaskContext, input map[string]any) (map[string]any, error)

type Function struct {
	ID string
	// WhenToUse is routing guidance for selecting this function.
	// Keep it non-redundant with Schema (describe intent/situation, not field shapes).
	WhenToUse string
	// Schema is a JSON Schema object represented as map[string]any.
	// It should follow the same Gemini-compatible JSON Schema subset used elsewhere
	// in this codebase (object/array/scalar types, properties, required, etc.).
	Schema map[string]any
	Exec   ExecFunc
}

type RunResult struct {
	FunctionID   string                              `json:"function_id"`
	Output       map[string]any                      `json:"output,omitempty"`
	Conversation []baselineagent.ConversationMessage `json:"-"`
}
