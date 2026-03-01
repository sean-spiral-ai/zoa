package baselineagent

// StructuredResponseFormat defines constrained-decoding output requirements
// for a single prompt call.
type StructuredResponseFormat interface {
	MimeType() string
	Schema() map[string]any
}

// JSONSchemaFormat requests JSON output constrained by provider-supported schema features.
// Gemini enforces this with responseSchema; Claude enforces this with output_config json_schema.
type JSONSchemaFormat struct {
	SchemaObject map[string]any
}

func (f JSONSchemaFormat) MimeType() string {
	return "application/json"
}

func (f JSONSchemaFormat) Schema() map[string]any {
	if f.SchemaObject == nil {
		return nil
	}
	return cloneStructuredMap(f.SchemaObject)
}

func cloneStructuredMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = cloneStructuredValue(v)
	}
	return out
}

func cloneStructuredValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return cloneStructuredMap(t)
	case []any:
		out := make([]any, len(t))
		for i := range t {
			out[i] = cloneStructuredValue(t[i])
		}
		return out
	default:
		return v
	}
}
