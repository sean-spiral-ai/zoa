package functions

import "fmt"

func stringInput(input map[string]any, key string, required bool) (string, error) {
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

func boolInput(input map[string]any, key string, required bool) (bool, error) {
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

func intInput(input map[string]any, key string, required bool) (int, error) {
	raw, ok := input[key]
	if !ok {
		if required {
			return 0, fmt.Errorf("missing required input: %s", key)
		}
		return 0, nil
	}
	switch v := raw.(type) {
	case int:
		return v, nil
	case float64:
		return int(v), nil
	default:
		return 0, fmt.Errorf("input %s must be number", key)
	}
}

func floatInput(input map[string]any, key string, required bool) (float64, error) {
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
