package builtintools

import toptools "zoa/tools"

type Tool = toptools.Tool
type Registry = toptools.Registry

func NewRegistry(toolset []Tool) *Registry {
	return toptools.NewRegistry(toolset)
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
