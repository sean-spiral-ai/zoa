# zoa

Zoa is a runtime for persistent AI-driven software systems, with durable conversations, task execution, and a monorepo `hub/` that stands in for a future ZoaHub.

## Layout

- [`conversation/`](/home/albion/pg/zoa/conversation): conversation DB, runner, and viewer server
- [`runtime/`](/home/albion/pg/zoa/runtime): task runtime, task logs, and model-facing ZoaFunction/ZoaMixin tools
- [`hub/`](/home/albion/pg/zoa/hub): monorepo package directory

Within `hub/`, the source layout is `hub/<package>/<module>/`.
In prose these correspond to ZoaPackage and ZoaModule. Runtime IDs stay `module.name`; package is only a source-management grouping.

Current examples:
- [`hub/core/intrinsic/`](/home/albion/pg/zoa/hub/core/intrinsic)
- [`hub/util/gateway/`](/home/albion/pg/zoa/hub/util/gateway)
- [`hub/util/diverse_ideation/`](/home/albion/pg/zoa/hub/util/diverse_ideation)
- [`hub/util/md_to_pdf/`](/home/albion/pg/zoa/hub/util/md_to_pdf)

## State

Persistent state is split into three SQLite files:

1. `conversation.db`
   Conversation history only: immutable `conversation_node` rows plus mutable `conversation_ref` heads.
2. `runtime.db`
   Runtime-owned execution state such as `runtime__task_log`.
3. `state.db`
   User or hub-module state exposed through `TaskContext.SqlExec`, `TaskContext.SqlQuery`, and `TaskContext.SqlTx`.

That boundary is intentional:
- [`conversation/`](/home/albion/pg/zoa/conversation) owns transcripts and branching.
- [`runtime/`](/home/albion/pg/zoa/runtime) owns orchestration and task lifecycle.
- [`hub/`](/home/albion/pg/zoa/hub) owns module state.

## Commands

```bash
go test ./...
go run ./cmd/zoa -- tui --cwd /absolute/workspace/path --session-dir .gateway/sessions/default
go run ./cmd/zoa -- inspect --session-dir .gateway/sessions/default
go run ./cmd/zoa -- inspect --session-dir .gateway/sessions/default conversation
```

## Notes

- Set `GEMINI_API_KEY` for Gemini models.
- Set `ANTHROPIC_API_KEY` or `ANTHROPIC_SETUP_TOKEN` for Anthropic models.
- `call_zoafunction` is asynchronous by design. If the model needs the result, it must call `wait_zoafunction`.
