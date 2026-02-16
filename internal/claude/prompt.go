package claude

import "github.com/eloylp/agents/internal/ai"

func BuildIssueRefinePrompt(repo string, number int) string {
	return ai.BuildIssueRefinePrompt(repo, number)
}

func BuildPRReviewPrompt(backend string, agent string, repo string, number int) string {
	return ai.BuildPRReviewPrompt(backend, agent, repo, number)
}
