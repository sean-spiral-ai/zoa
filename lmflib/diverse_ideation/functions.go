package diverse_ideation

import (
	"fmt"
	"strings"

	"zoa/lmflib"
	lmfrt "zoa/lmfrt"
)

const diverseIdeationPrompt = `You are generating a maximally diverse set of ideas.

Based on research showing Chain-of-Thought prompting achieves near-human-level idea diversity (cosine similarity 0.255 vs humans at 0.243, compared to naive prompting at 0.377+), you MUST follow the structured steps below exactly.

Context brief:
%s

Follow these steps. Do each step, even if you think you do not need to.

Step 1: Generate a list of %d ideas as short titles only (one line each). Cover as many different angles, mechanisms, and approaches as possible. No two ideas should address the same need in the same way.

Step 2: Review your list. For each idea, check: is it genuinely different from every other idea in primary mechanism AND target need? If two ideas feel similar, replace one with something from a completely different direction. Be aggressive about this — similarity is the enemy. This step is critical.

Step 3: For each idea, write a short description (2-4 sentences) covering: what it is, how it works, who it's for, and why it's different from obvious alternatives.

Format your final output as a numbered list with "Title: Description" format.

Remember: The goal is HIGH RECALL of the idea space, not filtering for quality. Bad-but-different ideas are better than good-but-similar ideas. Quality filtering happens later.`

func diverseIdeationFunction() *lmfrt.Function {
	return &lmfrt.Function{
		ID:        "diverse_ideation.diverse_ideation",
		WhenToUse: "Use when brainstorming to generate a maximally diverse set of ideas on any topic. Employs Chain-of-Thought prompting to avoid LLM mode collapse and produce high-variance output with genuinely different ideas rather than variations on the same theme. Best for business ideas, product concepts, strategies, solutions, or any creative task needing 30-50 diverse ideas. Always follow with a separate assessment/pruning step.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"brief": map[string]any{
					"type":        "string",
					"description": "Distilled context brief for ideation. Should include: domain/topic, constraints (scope, budget, feasibility), target audience, known solutions to avoid, evaluation criteria preview, and entropy sources (real-world data, trends, signals). The more specific and grounded, the more diverse AND relevant the output.",
				},
				"num_ideas": map[string]any{
					"type":        "integer",
					"description": "Number of ideas to generate. Sweet spot is 30-50 per session. Beyond ~100, exhaustion effects kick in. Defaults to 40.",
				},
			},
			"required": []string{"brief"},
		},
		OutputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"ideas":     map[string]any{"type": "string"},
				"num_ideas": map[string]any{"type": "integer"},
			},
			"required": []string{"ideas", "num_ideas"},
		},
		Exec: runDiverseIdeation,
	}
}

func runDiverseIdeation(tc *lmfrt.TaskContext, input map[string]any) (map[string]any, error) {
	brief, err := lmflib.StringInput(input, "brief", true)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(brief) == "" {
		return nil, fmt.Errorf("brief cannot be empty")
	}

	numIdeas, err := lmflib.IntInput(input, "num_ideas", false)
	if err != nil {
		return nil, err
	}
	if numIdeas <= 0 {
		numIdeas = 40
	}
	if numIdeas > 100 {
		numIdeas = 100
	}

	prompt := fmt.Sprintf(diverseIdeationPrompt, strings.TrimSpace(brief), numIdeas)

	finalResponse, err := tc.NLExec(prompt, nil)
	output := map[string]any{
		"ideas":     finalResponse,
		"num_ideas": numIdeas,
	}
	if strings.TrimSpace(finalResponse) == "" {
		if err != nil {
			return output, fmt.Errorf("diverse_ideation failed with empty response: %w", err)
		}
		return output, fmt.Errorf("diverse_ideation produced empty response")
	}
	if err != nil {
		return output, fmt.Errorf("diverse_ideation failed: %w", err)
	}
	return output, nil
}
