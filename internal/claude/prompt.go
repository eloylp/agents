package claude

import "github.com/eloylp/agents/internal/ai"

func BuildIssueRefinePrompt(repo string, number int, fingerprint string, labelGate string) string {
	return ai.BuildIssueRefinePrompt(repo, number, fingerprint, labelGate)
}

func BuildPRReviewPrompt(repo string, number int, fingerprint string, labelGate string) string {
	return ai.BuildPRReviewPrompt(repo, number, fingerprint, labelGate)
}
