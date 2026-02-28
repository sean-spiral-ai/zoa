package lmf

type ExecFunc func(tc *TaskContext, input map[string]any) (map[string]any, error)

type Function struct {
	ID          string
	Description string
	Exec        ExecFunc
}

type RunResult struct {
	FunctionID string         `json:"function_id"`
	Output     map[string]any `json:"output,omitempty"`
}
