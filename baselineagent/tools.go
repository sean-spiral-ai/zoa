package baselineagent

import (
	builtintools "zoa/baselineagent/builtintools"
	"zoa/llm"
	toptools "zoa/tools"
)

// ToolSpec describes a model-callable tool.
type ToolSpec = llm.ToolSpec

// Tool is the public tool interface used by baselineagent sessions.
type Tool = builtintools.Tool

// NewBuiltinCodingTools returns the baseline built-in coding tools
// (read/bash/edit/write/grep/find/ls).
func NewBuiltinCodingTools(cwd string) ([]Tool, error) {
	return toptools.NewCodingTools(cwd)
}

func StringArg(args map[string]any, key string, required bool) (string, error) {
	return toptools.StringArg(args, key, required)
}

func IntArg(args map[string]any, key string, required bool) (int, error) {
	return toptools.IntArg(args, key, required)
}

func BoolArg(args map[string]any, key string, required bool) (bool, error) {
	return toptools.BoolArg(args, key, required)
}
