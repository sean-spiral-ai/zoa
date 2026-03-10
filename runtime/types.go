package runtime

import "zoa/internal/agentloop/llm"

type ExecFunc func(tc *TaskContext, input map[string]any) (map[string]any, error)

type Function struct {
	ID string
	// WhenToUse is routing guidance for selecting this function.
	// Keep it non-redundant with InputSchema (describe intent/situation, not field shapes).
	WhenToUse string
	// InputSchema is a JSON Schema object represented as map[string]any.
	// It should follow the same JSON Schema subset used elsewhere
	// in this codebase (object/array/scalar types, properties, required, etc.).
	InputSchema map[string]any
	// OutputSchema describes the function output object shape when available.
	OutputSchema map[string]any
	// AssetsDir is an optional path to a directory of static assets bundled with this function.
	AssetsDir string
	Exec      ExecFunc
}

type RunResult struct {
	FunctionID   string         `json:"function_id"`
	Output       map[string]any `json:"output,omitempty"`
	Conversation []llm.Message  `json:"-"`
}

type Mixin struct {
	ID string
	// WhenToUse is routing guidance for selecting this mixin.
	WhenToUse string
	// Content is the context text loaded into ZoaFunction NL execution instructions.
	Content string
}
