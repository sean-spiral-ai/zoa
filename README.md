# codexAgentBase

This repository now has primary parts:

- `baselineagent/`: baseline coding agent (Gemini + Anthropic function-calling loop + core coding tools)
- `lmfrt/`: LM Function runtime
- `lmflib/`: LM Function library (`intrinsic.*` and `gateway.*`)
- `cmd/zoa/`: CLI entrypoint (`tui`, `inspect`)

## Baseline agent

The baseline agent is exposed as code:

- import path: `zoa/baselineagent`
- entrypoint: `baselineagent.Run(ctx, apiKey, cfg)`
- public tool API: `baselineagent.Tool`, `baselineagent.NewBuiltinCodingTools(...)`

## Gateway TUI

Run the persistent chat gateway:

```bash
go run ./cmd/zoa -- tui \
  --cwd /absolute/workspace/path \
  --session-dir .gateway/sessions/default
```

Inspect a gateway session database:

```bash
go run ./cmd/zoa -- inspect --session-dir .gateway/sessions/default
```

Run an explicit SQL query:

```bash
go run ./cmd/zoa -- inspect --session-dir .gateway/sessions/default "SELECT name FROM sqlite_master WHERE type='table' ORDER BY name"
```

Behavior:
- non-blocking input loop (each inbound message is handled via `gateway.recv`)
- slash commands handled programmatically: `/status`, `/queue`, `/log`, `/tasks`, `/outbox`
- on-disk persistence: SQLite (`state.db`) including gateway state and `lmfrt__task_log`
- session reload from disk on restart
- provider is inferred from an exact-model whitelist:
  - `claude-sonnet-4-6`
  - `claude-opus-4-6` (default)
  - `gemini-3.1-pro-preview`
  - `gemini-3-flash-preview`

## Tests

Run all tests with:

```bash
go test ./...
```

## Notes

- For Gemini, set `GEMINI_API_KEY`.
- For Anthropic OAuth, set `ANTHROPIC_OAUTH_TOKEN`.
- `intrinsic.modify_codebase` and LLM-backed tests require model credentials.
- There is no pre/post condition framework now; programmatic checks are regular Go errors in function bodies.
- `ctx.NLCondition(...)` evaluates NL checks in an isolated fork of the task's baselineagent conversation.
- `ctx.NLExec(...)` appends to one shared task conversation. Use `lmfrt.NLExecTyped[T](ctx, ...)` for typed JSON returns.
- Baseline agent default system prompt is intentionally short/generic; `intrinsic.modify_codebase` provides the full coding-focused system prompt.
- `NLExecTyped` requests JSON-only responses. Gemini uses `responseMimeType=application/json` + `responseSchema`; Claude uses native `output_config.format.type=json_schema`.
- LMF now includes a task runtime (`TaskManager`) and model-callable tools:
  - `search_lmfunctions`
  - `call_lmfunction`
  - `wait_lmfunction`
