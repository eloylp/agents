package claude

import "github.com/eloylp/agents/internal/ai"

func BuildIssueRefinePrompt(agent string, repo string, number int) string {
	return ai.BuildIssueRefinePrompt(agent, repo, number)
}

func BuildPRReviewPrompt(agent string, role string, repo string, number int) string {
	return ai.BuildPRReviewPrompt(agent, role, repo, number)
}
