# codexAgentBase

This repository now has three primary parts:

- `baselineagent/`: baseline coding agent (Gemini function-calling loop + core coding tools)
- `lmf/`: simple LM Function runtime and function registry
- `gateway/`: persistent chat-session ingress/egress layer with non-blocking TUI

## Baseline agent

The baseline agent is exposed as code:

- import path: `codexagentbase/baselineagent`
- entrypoint: `baselineagent.Run(ctx, apiKey, cfg)`
- public tool API: `baselineagent.Tool`, `baselineagent.NewBuiltinCodingTools(...)`

## Gateway TUI

Run the persistent chat gateway:

```bash
go run ./gateway/cmd/gateway-tui \
  --cwd /absolute/workspace/path \
  --session-dir .gateway/sessions/default
```

Behavior:
- non-blocking input loop (each inbound message is queued via `gateway.recv`)
- slash commands handled programmatically: `/status`, `/queue`, `/log`, `/tasks`, `/outbox`
- on-disk persistence: `snapshot.json` + `tasks/task-*.json`
- session reload from disk on restart

## Tests

Run all tests with:

```bash
go test ./...
```

## Notes

- `GEMINI_API_KEY` must be set for `intrinsic.modify_codebase` and LLM-backed tests.
- There is no pre/post condition framework now; programmatic checks are regular Go errors in function bodies.
- `ctx.NLCondition(...)` evaluates NL checks in an isolated fork of the task's baselineagent conversation.
- `ctx.NLExec(...)` appends to one shared task conversation. Use `lmf.NLExecTyped[T](ctx, ...)` for typed JSON returns.
- Baseline agent default system prompt is intentionally short/generic; `intrinsic.modify_codebase` provides the full coding-focused system prompt.
- `NLExecTyped` uses Gemini constrained decoding (`responseMimeType=application/json` + generated `responseSchema`) for schema-constrained outputs.
- LMF now includes a task runtime (`TaskManager`) and model-callable tools:
  - `search_lmfunctions`
  - `call_lmfunction`
  - `wait_lmfunction`
