package baselineagent

import (
	builtintools "zoa/baselineagent/builtintools"
	"zoa/baselineagent/internal/llm"
)

// ToolSpec describes a model-callable tool.
type ToolSpec = llm.ToolSpec

// Tool is the public tool interface used by baselineagent sessions.
type Tool = builtintools.Tool

// NewBuiltinCodingTools returns the baseline built-in coding tools
// (read/bash/edit/write/grep/find/ls).
func NewBuiltinCodingTools(cwd string) ([]Tool, error) {
	return builtintools.NewCodingTools(cwd)
}

func StringArg(args map[string]any, key string, required bool) (string, error) {
	return builtintools.StringArg(args, key, required)
}

func IntArg(args map[string]any, key string, required bool) (int, error) {
	return builtintools.IntArg(args, key, required)
}

func BoolArg(args map[string]any, key string, required bool) (bool, error) {
	return builtintools.BoolArg(args, key, required)
}
