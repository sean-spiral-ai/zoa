# Baseline Agent

A practical coding agent with:

- Gemini + Anthropic function-calling clients
- agent tool loop
- tools: read, write, edit, bash, grep, find, ls

Library:

- package: `zoa/baselineagent`
- API: `baselineagent.Run(ctx, apiKey, baselineagent.RunConfig{...})`
- custom tools: `baselineagent.Tool`
- builtin tool set: `baselineagent.NewBuiltinCodingTools(cwd)`
