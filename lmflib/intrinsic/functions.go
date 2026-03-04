package intrinsic

import (
	"fmt"
	"strings"

	"zoa/lmflib"
	lmfrt "zoa/lmfrt"
)

const intrinsicCreateLMFunctionPrompt = `You are creating a new LM Function in this repository.

LM Function system context:
- LM Functions are reusable workflows executed by the LM Function Runtime.
- They can combine programmatic execution (Go code) and natural-language/agentic execution (TaskContext NLExec/NLCondition).
- Treat LM Functions as composable building blocks that are clear, testable, and narrowly scoped.
- LM Functions live within the zoa project (often located at /projects/common/zoa).

Repository conventions to follow:
- LM Function implementations live under lmflib/<namespace>/.
- Define functions in lmflib/<namespace>/functions.go.
- Register them in lmflib/<namespace>/register.go.
- Use IDs like "<namespace>.<action>".
- Include strong WhenToUse guidance and explicit JSON schemas.
- Validate and normalize inputs; return stable structured outputs.
- Keep behavior deterministic where possible and avoid hidden side effects.

What LM Functions look like in code (brief):
- Return a *lmfrt.Function (Go struct) with:
  - ID
  - WhenToUse
  - InputSchema / OutputSchema
  - Exec: func(tc *lmfrt.TaskContext, input map[string]any) (map[string]any, error)
- Example shape:
  func myFunction() *lmfrt.Function { ... }
  func runMyFunction(tc *lmfrt.TaskContext, input map[string]any) (map[string]any, error) { ... }
- Register in RegisterFunctions(...) via registry.Register(myFunction()).

TaskContext reference:
- Read lmfrt/task_context.go to understand available runtime APIs:
  - SqlExec / SqlQuery / SqlTx
  - Spawn / RegisterPump / NewLmFunctionTools
  - NLExec / NLExecTyped / NLCondition

Reference implementations:
- lmflib/gateway/functions.go (multi-function namespace with stateful runtime behavior)
- lmflib/intrinsic/functions.go (intrinsic namespace style and NLExec-based execution)

Execution requirements:
- Inspect existing code before editing.
- Make minimal, production-quality changes consistent with surrounding style.
- Add or update tests when appropriate.
- Run targeted validation commands (at least relevant go test packages).

Final response:
- Briefly summarize created/updated files, function IDs added, and validation results.
`

func intrinsicCreateLMFunction() *lmfrt.Function {
	return &lmfrt.Function{
		ID:        "intrinsic.create_lmfunction",
		WhenToUse: "Use when you need to add or update LM Functions in this repository following lmflib namespace conventions and runtime integration patterns.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"cwd":         map[string]any{"type": "string", "description": "Current working directory for the task"},
				"instruction": map[string]any{"type": "string", "description": "What LM Function(s) to create or update"},
			},
			"required": []string{"cwd", "instruction"},
		},
		OutputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"final_response": map[string]any{"type": "string"},
			},
			"required": []string{"final_response"},
		},
		Exec: runIntrinsicCreateLMFunction,
	}
}

func runIntrinsicCreateLMFunction(tc *lmfrt.TaskContext, input map[string]any) (map[string]any, error) {
	return runIntrinsicPromptTask(tc, input, "intrinsic.create_lmfunction", intrinsicCreateLMFunctionPrompt)
}

func runIntrinsicPromptTask(tc *lmfrt.TaskContext, input map[string]any, functionID string, systemPrompt string) (map[string]any, error) {
	cwd, err := lmflib.StringInput(input, "cwd", true)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(cwd) == "" {
		return nil, fmt.Errorf("cwd cannot be empty")
	}

	instruction, err := lmflib.StringInput(input, "instruction", true)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(instruction) == "" {
		return nil, fmt.Errorf("instruction cannot be empty")
	}

	combinedTask := fmt.Sprintf(`%s

Complete this coding task in the current workspace:
%s
`, systemPrompt, instruction)

	finalResponse, err := tc.NLExec(combinedTask, nil)
	output := map[string]any{
		"final_response": finalResponse,
	}
	if strings.TrimSpace(finalResponse) == "" {
		if err != nil {
			return output, fmt.Errorf("%s failed with empty final_response: %w", functionID, err)
		}
		return output, fmt.Errorf("%s produced empty final_response", functionID)
	}
	if err != nil {
		return output, fmt.Errorf("%s failed: %w", functionID, err)
	}
	return output, nil
}
