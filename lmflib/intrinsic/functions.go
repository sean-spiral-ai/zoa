package intrinsic

import lmfrt "zoa/lmfrt"

const lmFunctionSystemMixinContent = `LM Function system context:
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
  - LoadMixin
  - NLExec / NLExecTyped / NLCondition

Reference implementations:
- lmflib/gateway/functions.go (multi-function namespace with stateful runtime behavior)
- lmflib/diverse_ideation/functions.go (single-function namespace style)

Execution requirements:
- Inspect existing code before editing.
- Make minimal, production-quality changes consistent with surrounding style.
- Add or update tests when appropriate.
- Run targeted validation commands (at least relevant go test packages).
`

func lmFunctionSystemMixin() *lmfrt.Mixin {
	return &lmfrt.Mixin{
		ID:        "intrinsic.lmfunction_system",
		WhenToUse: "Use when you need authoritative context about LM Function architecture, conventions, and TaskContext APIs while designing or editing LM Functions.",
		Content:   lmFunctionSystemMixinContent,
	}
}
