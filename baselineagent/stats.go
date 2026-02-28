package baselineagent

import "codexagentbase/baselineagent/internal/llm"

func countToolCalls(messages []llm.Message) int {
	total := 0
	for _, m := range messages {
		total += len(m.ToolCalls)
	}
	return total
}
