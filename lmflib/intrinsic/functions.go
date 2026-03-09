package intrinsic

import "zoa/runtime"

const lmFunctionSystemMixinContent = `LM Function system context:
- LM Functions are reusable workflows executed by the LM Function Runtime.
- LM Mixins are reusable context snippets that can be loaded into the context window for future NL execution.
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
- Return a *runtime.Function (Go struct) with:
  - ID
  - WhenToUse
  - InputSchema / OutputSchema
  - Exec: func(tc *runtime.TaskContext, input map[string]any) (map[string]any, error)
- Example shape:
  func myFunction() *runtime.Function { ... }
  func runMyFunction(tc *runtime.TaskContext, input map[string]any) (map[string]any, error) { ... }
- Register in RegisterFunctions(...) via registry.Register(myFunction()).

TaskContext reference:
- Read runtime/task_context.go to understand available runtime APIs:
  - SqlExec / SqlQuery / SqlTx
  - Spawn / RegisterPump / NewLmFunctionTools
  - LoadMixin
  - NLExec / NLExecTyped / NLCondition
  - GetStateDir / GetTmpDir / GetAssetsDir

Filesystem APIs (GetStateDir, GetTmpDir, GetAssetsDir):
- GetStateDir() returns a persistent directory at <sessionDir>/namespace_state/<namespace>/.
  Use for venvs, caches, downloaded models, or anything that should survive across invocations.
  Created automatically on first call. Namespace is derived from the function ID prefix.
- GetTmpDir() returns a fresh temporary directory, auto-removed when TaskContext.Close() runs.
  Use for intermediate/scratch files that the caller doesn't need after the function returns.
- GetAssetsDir() returns the path to lmflib/<namespace>/assets/ in the source tree.
  Use for bundled scripts, stylesheets, configs, or other static files shipped with the function.
  Set by populating AssetsDir on *runtime.Function (typically via runtime.Caller in register.go).
- Common pattern: copy assets into the state dir on first run, then operate from the state dir.
  See lmflib/md_to_pdf/ for a reference: it copies a Python script + CSS to the state dir,
  creates a venv there, installs deps once (guarded by a marker file), and runs from the state dir.

Reference implementations:
- lmflib/gateway/functions.go (multi-function namespace with stateful runtime behavior)
- lmflib/diverse_ideation/functions.go (single-function namespace style)
- lmflib/md_to_pdf/functions.go (external script with venv, assets, and state dir)

Execution requirements:
- Inspect existing code before editing.
- Make minimal, production-quality changes consistent with surrounding style.
- Add or update tests when appropriate.
- Run targeted validation commands (at least relevant go test packages).
`

func lmFunctionSystemMixin() *runtime.Mixin {
	return &runtime.Mixin{
		ID:        "intrinsic.lmfunction_system",
		WhenToUse: "Use when you need authoritative context about LM Function architecture, LM Mixins, conventions, and TaskContext APIs while designing or editing LM Functions.",
		Content:   lmFunctionSystemMixinContent,
	}
}
