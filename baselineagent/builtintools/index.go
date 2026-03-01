package builtintools

func NewCodingTools(cwd string) ([]Tool, error) {
	paths, err := NewPathResolver(cwd)
	if err != nil {
		return nil, err
	}
	return []Tool{
		NewReadTool(paths),
		NewBashTool(paths),
		NewEditTool(paths),
		NewWriteTool(paths),
		NewGrepTool(paths),
		NewFindTool(paths),
		NewLsTool(paths),
	}, nil
}
