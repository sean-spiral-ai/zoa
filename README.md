# zoa

Zoa is an LM-function runtime with a persistent gateway, a conversation store, and a library of LM functions in [`lmflib/`](/home/albion/pg/zoa/lmflib).

## Architecture

Persistent state is intentionally split into three layers:

1. `conversation.db`
   Stores only conversation state from [`conversation/db/`](/home/albion/pg/zoa/conversation/db): immutable `conversation_node` rows and mutable `conversation_ref` rows. Gateway chat sessions and runtime `NLExec`/`NLCondition` flows use this DB.

2. `runtime.db`
   Stores runtime-owned execution state from [`runtime/`](/home/albion/pg/zoa/runtime), primarily `runtime__task_log` and related orchestration metadata. Runtime state may point into conversation refs or hashes, but it should not own full transcripts.

3. `state.db`
   Stores LMFunction or application "user state". This is the DB reached by `TaskContext.SqlExec`, `TaskContext.SqlQuery`, and `TaskContext.SqlTx`. Tables under [`lmflib/`](/home/albion/pg/zoa/lmflib) such as gateway inbound/outbox state belong here.

The intended ownership boundary is:

- [`conversation/`](/home/albion/pg/zoa/conversation) owns conversational history and branching.
- [`runtime/`](/home/albion/pg/zoa/runtime) owns task execution, scheduling, and logs.
- [`lmflib/`](/home/albion/pg/zoa/lmflib) owns domain or user-facing state.

## Layout

- [`conversation/`](/home/albion/pg/zoa/conversation): conversation DB, runner, and viewer server
- [`runtime/`](/home/albion/pg/zoa/runtime): task runtime, task logs, LMFunction tools
- [`lmflib/`](/home/albion/pg/zoa/lmflib): LMFunction library
- [`cmd/zoa/`](/home/albion/pg/zoa/cmd/zoa): CLI entrypoints
- [`internal/slack/`](/home/albion/pg/zoa/internal/slack): Slack bridge
- [`internal/daemon/`](/home/albion/pg/zoa/internal/daemon): daemon management

## Gateway Session Files

A gateway session directory such as `.gateway/sessions/default/` now contains:

- `conversation.db`: conversation refs and nodes
- `runtime.db`: runtime task logs and runtime-owned metadata
- `state.db`: gateway/user-state tables such as `gateway__inbound` and `gateway__outbox`

## Commands

Run tests:

```bash
go test ./...
```

Run the TUI gateway:

```bash
go run ./cmd/zoa -- tui --cwd /absolute/workspace/path --session-dir .gateway/sessions/default
```

Inspect user state:

```bash
go run ./cmd/zoa -- inspect --session-dir .gateway/sessions/default
```

Inspect a conversation transcript:

```bash
go run ./cmd/zoa -- inspect --session-dir .gateway/sessions/default conversation
```

## Notes

- Set `GEMINI_API_KEY` for Gemini models.
- Set `ANTHROPIC_OAUTH_TOKEN` for Anthropic models.
- `call_lmfunction` is asynchronous by design. If the model needs the result, it must call `wait_lmfunction`.
