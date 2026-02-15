package claude

import "github.com/eloylp/agents/internal/ai"

func BuildIssueRefinePrompt(agent string, repo string, number int, fingerprint string, labelGate string) string {
	return ai.BuildIssueRefinePrompt(agent, repo, number, fingerprint, labelGate)
}

func BuildPRReviewPrompt(agent string, role string, repo string, number int, fingerprint string, labelGate string) string {
	return ai.BuildPRReviewPrompt(agent, role, repo, number, fingerprint, labelGate)
}
