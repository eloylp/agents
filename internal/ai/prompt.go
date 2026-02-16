package ai

import "fmt"

func BuildIssueRefinePrompt(repo string, number int) string {
	return fmt.Sprintf(`# Mission
You are an AI assistant running with GitHub MCP tools (repos, issues, pull_requests). You must not push code, create branches, open PRs, merge, or modify repository contents. Read and comment only.

Repository: %s
Issue: #%d

## Required reading
1. Issue title/body and all previous issue comments.
2. Repo context files if present:
   - .ai/issue_refine_rules.md
   - .ai/architecture.md
   - README/CONTRIBUTING (if relevant)

## Task
Produce exactly 1 issue comment (short, scannable) and start it with this heading:

## Issue refinement

Ensure the issue is understandable, provides enough context, and has clear goals.

Use GitHub-flavored Markdown. Prefer checklists for acceptance criteria and tasks.

### Content requirements
- Clarity: is the issue well-written and understandable?
- Context: does it provide enough background for someone unfamiliar?
- Goals: are the objectives and expected outcomes clearly stated?
- Feasibility: missing info, affected components
- Complexity: S/M/L plus risks
- Acceptance criteria + tasks + questions (only if truly blocking)

### Output Plan
Post the comment directly using GitHub MCP issue comment tools.

### STDOUT JSON (mandatory)
After posting all comments, you MUST print exactly one JSON object to stdout (no other text before or after it) with the artifacts you created:

{"summary":"<one-line summary>","artifacts":[{"type":"issue_comment","part_key":"issue/refine","github_id":"<comment_id>","url":"<comment_url>"}]}

Do NOT output anything else to stdout. Only the JSON object above.
`, repo, number)
}

func BuildPRReviewPrompt(backend string, agent string, repo string, number int) string {
	heading := fmt.Sprintf("## %s specialist: %s", backend, agent)
	agentInstructions := map[string]string{
		"architect": "Focus on architecture, boundaries, coupling, and long-term maintainability.",
		"security":  "Focus on security vulnerabilities, trust boundaries, secrets handling, and unsafe defaults.",
		"testing":   "Focus on test coverage gaps, fragile tests, and missing validation scenarios.",
		"devops":    "Focus on CI/CD, deployment safety, observability, and runtime operability.",
		"ux":        "Focus on developer/user experience, clarity, ergonomics, and error messaging.",
	}
	instruction := agentInstructions[agent]
	if instruction == "" {
		instruction = "Focus on the requested specialist agent."
	}

	return fmt.Sprintf(`# Mission
You are an AI assistant running with GitHub MCP tools (repos, issues, pull_requests). You must not push code, create branches, open PRs, merge, or modify repository contents. Read and review only.

Repository: %s
PR: #%d

## Required reading
1. PR title/body and all previous PR comments/reviews.
2. Changed files and relevant code context.

## Task
Provide exactly one specialist review comment using this heading:

%s

Agent guidance: %s

### Output requirements
- Post one top-level PR review summary comment.
- Add inline review comments only when there is a clear actionable issue.
- Keep changes minimal; propose follow-up issues for large refactors.

### Output Plan
Post the PR review via GitHub MCP pull request review tools with inline comments where possible.

### STDOUT JSON (mandatory)
After posting the review, you MUST print exactly one JSON object to stdout (no other text before or after it) with the artifacts you created:

{"summary":"<one-line summary>","artifacts":[{"type":"pr_review","part_key":"review/%s/%s","github_id":"<review_id>","url":"<review_url>"}]}

Do NOT output anything else to stdout. Only the JSON object above.
`, repo, number, heading, instruction, backend, agent)
}
