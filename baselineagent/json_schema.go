package baselineagent

import "zoa/llm"

func JSONSchemaForOutputValue(out any) (map[string]any, error) {
	return llm.JSONSchemaForOutputValue(out)
}
