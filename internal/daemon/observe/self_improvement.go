package observe

import "github.com/eloylp/agents/internal/store"

func buildRecommendation(feedback store.SelfImprovementFeedback, st *store.Store) store.SelfImprovementRecommendationInput {
	in := store.RecommendationFromFeedback(feedback)
	promptVersionID := ""
	if prompt, err := st.ReadPrompt("prompt_self-improvement-analyst"); err == nil {
		promptVersionID = prompt.VersionID
	}
	in.AnalyzerPromptVersionID = promptVersionID
	if in.StructuredOutput != nil {
		in.StructuredOutput["analyzer_prompt_version"] = promptVersionID
	}
	return in
}
