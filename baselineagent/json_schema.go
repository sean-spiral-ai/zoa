package baselineagent

import (
	"fmt"
	"reflect"
	"strings"
)

// JSONSchemaForOutputValue builds a portable JSON schema from an
// output pointer type (for example: &MyStruct{} or new(int)).
func JSONSchemaForOutputValue(out any) (map[string]any, error) {
	if out == nil {
		return nil, fmt.Errorf("typed output target cannot be nil")
	}
	t := reflect.TypeOf(out)
	if t.Kind() != reflect.Pointer || t.Elem() == nil {
		return nil, fmt.Errorf("typed output target must be a non-nil pointer")
	}
	return schemaForType(t.Elem())
}

func schemaForType(t reflect.Type) (map[string]any, error) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	switch t.Kind() {
	case reflect.Bool:
		return map[string]any{"type": "boolean"}, nil
	case reflect.String:
		return map[string]any{"type": "string"}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}, nil
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}, nil
	case reflect.Slice, reflect.Array:
		items, err := schemaForType(t.Elem())
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "array", "items": items}, nil
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			return nil, fmt.Errorf("map output type must have string keys, got %s", t.Key())
		}
		// For map outputs we fall back to unconstrained object shape for cross-provider compatibility.
		return map[string]any{"type": "object"}, nil
	case reflect.Struct:
		properties := map[string]any{}
		required := []string{}
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if field.PkgPath != "" { // unexported
				continue
			}
			name, optional, skip := jsonFieldName(field)
			if skip {
				continue
			}
			fieldSchema, err := schemaForType(field.Type)
			if err != nil {
				return nil, fmt.Errorf("field %s: %w", field.Name, err)
			}
			properties[name] = fieldSchema
			if !optional {
				required = append(required, name)
			}
		}
		schema := map[string]any{
			"type":       "object",
			"properties": properties,
		}
		if len(required) > 0 {
			schema["required"] = required
		}
		return schema, nil
	default:
		return nil, fmt.Errorf("unsupported output type for schema generation: %s", t.String())
	}
}

func jsonFieldName(field reflect.StructField) (name string, optional bool, skip bool) {
	tag := field.Tag.Get("json")
	if tag == "-" {
		return "", false, true
	}
	if tag == "" {
		return field.Name, false, false
	}
	parts := strings.Split(tag, ",")
	if parts[0] == "" {
		name = field.Name
	} else {
		name = parts[0]
	}
	optional = false
	for _, part := range parts[1:] {
		if part == "omitempty" {
			optional = true
			break
		}
	}
	return name, optional, false
}
