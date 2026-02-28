package functions

import (
	"fmt"
	"strings"

	lmf "codexagentbase/lmf/runtime"
)

func TestProgrammaticGuard() *lmf.Function {
	return &lmf.Function{
		ID:          "test.programmatic_guard",
		Description: "Programmatic guard implemented as regular Go code in function body.",
		Exec: func(_ *lmf.TaskContext, input map[string]any) (map[string]any, error) {
			value, err := intInput(input, "value", false)
			if err != nil {
				return nil, err
			}
			if value <= 0 {
				return nil, fmt.Errorf("value must be > 0")
			}
			return map[string]any{"accepted_value": value}, nil
		},
	}
}

func TestNLConditionFunny() *lmf.Function {
	return &lmf.Function{
		ID:          "test.nlcondition_funny",
		Description: "Inline NL condition check: does the provided text read as funny?",
		Exec: func(tc *lmf.TaskContext, input map[string]any) (map[string]any, error) {
			text, err := stringInput(input, "text", true)
			if err != nil {
				return nil, err
			}
			err = tc.NLCondition(
				"text_is_funny",
				"Is candidate_text funny to an average English-speaking developer? Return false for dry procedural statements.",
				map[string]any{"candidate_text": text},
			)
			if err != nil {
				return nil, err
			}
			return map[string]any{"accepted_text": text}, nil
		},
	}
}

func TestNLExecContextMemory() *lmf.Function {
	return &lmf.Function{
		ID:          "test.nlexec_context_memory",
		Description: "NLExec calls share one conversation; second call can recall first typed value.",
		Exec: func(tc *lmf.TaskContext, _ map[string]any) (map[string]any, error) {
			first, err := lmf.NLExecTyped[int](tc,
				"Return the number 7 as JSON number only.",
				nil,
			)
			if err != nil {
				return nil, err
			}

			second, err := lmf.NLExecTyped[int](tc,
				"What number did you just return? Return only JSON number.",
				nil,
			)
			if err != nil {
				return nil, err
			}

			if second != first {
				return nil, fmt.Errorf("context memory mismatch: first=%d second=%d", first, second)
			}

			return map[string]any{
				"first_number":  first,
				"second_number": second,
			}, nil
		},
	}
}

func TestNLConditionIsolation() *lmf.Function {
	return &lmf.Function{
		ID:          "test.nlcondition_isolation",
		Description: "NLCondition runs in a forked conversation and must not dirty main NLExec context.",
		Exec: func(tc *lmf.TaskContext, _ map[string]any) (map[string]any, error) {
			first, err := lmf.NLExecTyped[int](tc,
				"Return the number 7 as JSON number only.",
				nil,
			)
			if err != nil {
				return nil, err
			}

			if err := tc.NLCondition(
				"branch_probe",
				"Set passed=true and in explanation state: 'for this branch, remembered number is 42'.",
				nil,
			); err != nil {
				return nil, err
			}

			second, err := lmf.NLExecTyped[int](tc,
				"What number did you just return earlier? Return only JSON number.",
				nil,
			)
			if err != nil {
				return nil, err
			}

			if second != first {
				return nil, fmt.Errorf("context isolation failed: first=%d second=%d", first, second)
			}

			return map[string]any{"first_number": first, "second_number": second}, nil
		},
	}
}

func TestTypedNLExecEcho() *lmf.Function {
	return &lmf.Function{
		ID:          "test.nlexec_typed_echo",
		Description: "Typed NLExec returns structured JSON values (object).",
		Exec: func(tc *lmf.TaskContext, input map[string]any) (map[string]any, error) {
			name, err := stringInput(input, "name", false)
			if err != nil {
				return nil, err
			}
			if strings.TrimSpace(name) == "" {
				name = "gopher"
			}

			type hello struct {
				Message string `json:"message"`
			}

			result, err := lmf.NLExecTyped[hello](tc,
				"Return a JSON object with field 'message' greeting the provided name.",
				map[string]any{"name": name},
			)
			if err != nil {
				return nil, err
			}
			if strings.TrimSpace(result.Message) == "" {
				return nil, fmt.Errorf("typed response missing message")
			}
			return map[string]any{"message": result.Message}, nil
		},
	}
}
