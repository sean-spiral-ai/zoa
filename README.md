# codexAgentBase

This repository now has three primary parts:

- `baselineagent/`: baseline coding agent (Gemini + Anthropic function-calling loop + core coding tools)
- `lmfrt/`: LM Function runtime
- `lmflib/`: LM Function library (`intrinsic.*` and `gateway.*`)
- `gateway/`: persistent chat-session ingress/egress layer with non-blocking TUI
- `tui/`: terminal UI entrypoints

## Baseline agent

The baseline agent is exposed as code:

- import path: `zoa/baselineagent`
- entrypoint: `baselineagent.Run(ctx, apiKey, cfg)`
- public tool API: `baselineagent.Tool`, `baselineagent.NewBuiltinCodingTools(...)`

## Gateway TUI

Run the persistent chat gateway:

```bash
go run ./tui/cmd \
  --cwd /absolute/workspace/path \
  --session-dir .gateway/sessions/default
```

Behavior:
- non-blocking input loop (each inbound message is queued via `gateway.recv`)
- slash commands handled programmatically: `/status`, `/queue`, `/log`, `/tasks`, `/outbox`
- on-disk persistence: `snapshot.json` + `tasks/task-*.json`
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
