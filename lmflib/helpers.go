package lmflib

import "fmt"

func StringInput(input map[string]any, key string, required bool) (string, error) {
	raw, ok := input[key]
	if !ok {
		if required {
			return "", fmt.Errorf("missing required input: %s", key)
		}
		return "", nil
	}
	s, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("input %s must be string", key)
	}
	if required && s == "" {
		return "", fmt.Errorf("input %s cannot be empty", key)
	}
	return s, nil
}

func BoolInput(input map[string]any, key string, required bool) (bool, error) {
	raw, ok := input[key]
	if !ok {
		if required {
			return false, fmt.Errorf("missing required input: %s", key)
		}
		return false, nil
	}
	b, ok := raw.(bool)
	if !ok {
		return false, fmt.Errorf("input %s must be bool", key)
	}
	return b, nil
}

func IntInput(input map[string]any, key string, required bool) (int, error) {
	v, err := Int64Input(input, key, required)
	if err != nil {
		return 0, err
	}
	return int(v), nil
}

func Int64Input(input map[string]any, key string, required bool) (int64, error) {
	raw, ok := input[key]
	if !ok {
		if required {
			return 0, fmt.Errorf("missing required input: %s", key)
		}
		return 0, nil
	}
	switch v := raw.(type) {
	case int:
		return int64(v), nil
	case int64:
		return v, nil
	case float64:
		return int64(v), nil
	default:
		return 0, fmt.Errorf("input %s must be integer", key)
	}
}

func FloatInput(input map[string]any, key string, required bool) (float64, error) {
	raw, ok := input[key]
	if !ok {
		if required {
			return 0, fmt.Errorf("missing required input: %s", key)
		}
		return 0, nil
	}
	switch v := raw.(type) {
	case float64:
		return v, nil
	case int:
		return float64(v), nil
	default:
		return 0, fmt.Errorf("input %s must be number", key)
	}
}

func Int64FromValue(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		return int64(n), true
	default:
		return 0, false
	}
}
