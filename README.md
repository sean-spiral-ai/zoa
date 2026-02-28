# codexAgentBase

This repository now has two primary parts:

- `baselineagent/`: baseline coding agent (Gemini function-calling loop + core coding tools)
- `lmf/`: simple LM Function runtime, function registry, and test CLI

## Baseline agent

Run directly:

```bash
go run ./baselineagent/cmd/coding-agent \
  --model gemini-3-flash-preview \
  --cwd /absolute/workspace/path \
  --prompt "Create a small project and run tests" \
  --verbose
```

The baseline agent is also exposed as code:

- import path: `codexagentbase/baselineagent`
- entrypoint: `baselineagent.Run(ctx, apiKey, cfg)`

## LM Function CLI

List functions:

```bash
go run ./lmf/cmd/lmf-cli list
```

Run one function:

```bash
go run ./lmf/cmd/lmf-cli run test.programmatic_guard --input-json '{"value":3}'
```

Run built-in tests for condition semantics:

```bash
go run ./lmf/cmd/lmf-cli run-all-tests
```

Run built-in tests + intrinsic baselineagent wrapper:

```bash
go run ./lmf/cmd/lmf-cli run-all-tests \
  --intrinsic-cwd /absolute/workspace/path \
  --intrinsic-instruction "Create a tiny Go CLI and run go test"
```

## Notes

- `GEMINI_API_KEY` must be set for `intrinsic.modify_codebase` and baseline agent runs.
- There is no pre/post condition framework now; programmatic checks are regular Go errors in function bodies.
- `ctx.NLCondition(...)` evaluates NL checks in an isolated fork of the task's baselineagent conversation.
- `ctx.NLExec(...)` appends to one shared task conversation. Use `lmf.NLExecTyped[T](ctx, ...)` for typed JSON returns.
- Baseline agent default system prompt is intentionally short/generic; `intrinsic.modify_codebase` provides the full coding-focused system prompt.
- `NLExecTyped` uses Gemini constrained decoding (`responseMimeType=application/json` + generated `responseSchema`) for schema-constrained outputs.
