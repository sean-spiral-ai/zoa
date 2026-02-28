# Baseline Agent

A practical coding agent with:

- Gemini function-calling client
- agent tool loop
- tools: read, write, edit, bash, grep, find, ls

CLI:

```bash
go run ./baseline-agent/cmd/coding-agent --prompt "..."
```

Library:

- package: `codexagentbase/baseline-agent`
- API: `baselineagent.Run(ctx, apiKey, baselineagent.RunConfig{...})`
