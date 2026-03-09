package builtintools

import toptools "zoa/tools"

func NewCodingTools(cwd string) ([]Tool, error) {
	return toptools.NewCodingTools(cwd)
}
