package baselineagent

import "codexagentbase/baseline-agent/internal/llm"

func countToolCalls(messages []llm.Message) int {
	total := 0
	for _, m := range messages {
		total += len(m.ToolCalls)
	}
	return total
}
