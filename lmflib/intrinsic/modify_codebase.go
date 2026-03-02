package intrinsic

import (
	"fmt"
	"strings"

	lmfrt "zoa/lmfrt"
)

const intrinsicModifyCodebasePrompt = `You are a pragmatic coding agent working inside a real codebase.

Core behavior:
- Inspect relevant files before editing.
- Make minimal, correct changes that satisfy the request.
- Prefer surgical edits with the edit tool; use write for new files or full rewrites.
- Run validation commands (build/tests/lint) when appropriate.

Tool guidance:
- Paths are relative to workspace root unless absolute under root.
- Read output can be truncated; continue with offset when needed.
- If a tool fails, adapt and retry with better arguments.
- When you decide to call tools, first provide a short Reason: line (one sentence) explaining why the next tool call(s) are needed.
- Validate your changes (for Go tasks, run go test or equivalent).

Final response:
- Summarize what changed and validation status.
`

func IntrinsicModifyCodebase() *lmfrt.Function {
	return &lmfrt.Function{
		ID:        "intrinsic.modify_codebase",
		WhenToUse: "Use when you need autonomous code edits in a workspace with verification (build/test/lint) and a human-readable change summary.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"cwd":         map[string]any{"type": "string", "description": "Current working directory for the task"},
				"instruction": map[string]any{"type": "string", "description": "The coding task to perform"},
			},
			"required": []string{"cwd", "instruction"},
		},
		Exec: runIntrinsicModifyCodebase,
	}
}

func runIntrinsicModifyCodebase(tc *lmfrt.TaskContext, input map[string]any) (map[string]any, error) {
	cwd, err := stringInput(input, "cwd", true)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(cwd) == "" {
		return nil, fmt.Errorf("cwd cannot be empty")
	}

	instruction, err := stringInput(input, "instruction", true)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(instruction) == "" {
		return nil, fmt.Errorf("instruction cannot be empty")
	}

	combinedTask := fmt.Sprintf(`%s

Complete this coding task in the current workspace:
%s
`, intrinsicModifyCodebasePrompt, instruction)

	finalResponse, err := tc.NLExec(combinedTask, nil)
	output := map[string]any{
		"final_response": finalResponse,
	}
	if strings.TrimSpace(finalResponse) == "" {
		if err != nil {
			return output, fmt.Errorf("intrinsic.modify_codebase failed with empty final_response: %w", err)
		}
		return output, fmt.Errorf("intrinsic.modify_codebase produced empty final_response")
	}
	if err != nil {
		return output, fmt.Errorf("intrinsic.modify_codebase failed: %w", err)
	}
	return output, nil
}
