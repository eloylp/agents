package ai

import "fmt"

func BuildIssueRefinePrompt(agent string, repo string, number int, fingerprint string) string {
	heading := fmt.Sprintf("## %s refinement", agent)

	return fmt.Sprintf(`# Mission
You are an AI assistant running with GitHub MCP tools (repos, issues, pull_requests). You must not push code, create branches, open PRs, merge, or modify repository contents. Read and comment only.

Repository: %s
Issue: #%d
Fingerprint: %s

## Required reading
1. Issue title/body and recent comments.
2. Repo context files if present:
   - .ai/issue_refine_rules.md
   - .ai/architecture.md
   - README/CONTRIBUTING (if relevant)

## Task
Produce exactly 1 issue comment (short, scannable) and start it with this heading:

%s

The comment must include this footer marker:

<!-- ai-daemon:issue-refine v1; fingerprint=%s; part=1/1 -->

Use GitHub-flavored Markdown. Prefer checklists for acceptance criteria and tasks.

### Content requirements
- Feasibility: missing info, affected components
- Complexity: S/M/L plus risks
- Recommended approach + alternatives
- Acceptance criteria + tasks + questions (only if truly blocking)

### Output Plan
Post the comment directly using GitHub MCP issue comment tools.

### STDOUT JSON (mandatory)
After posting all comments, you MUST print exactly one JSON object to stdout (no other text before or after it) with the artifacts you created:

{"summary":"<one-line summary>","artifacts":[{"type":"issue_comment","part_key":"issue/%s","github_id":"<comment_id>","url":"<comment_url>"}]}

Do NOT output anything else to stdout. Only the JSON object above.
`, repo, number, fingerprint, heading, fingerprint, agent)
}

func BuildPRReviewPrompt(agent string, role string, repo string, number int, fingerprint string) string {
	heading := fmt.Sprintf("## %s specialist: %s", agent, role)
	roleInstructions := map[string]string{
		"architect": "Focus on architecture, boundaries, coupling, and long-term maintainability.",
		"security":  "Focus on security vulnerabilities, trust boundaries, secrets handling, and unsafe defaults.",
		"testing":   "Focus on test coverage gaps, fragile tests, and missing validation scenarios.",
		"devops":    "Focus on CI/CD, deployment safety, observability, and runtime operability.",
		"ux":        "Focus on developer/user experience, clarity, ergonomics, and error messaging.",
	}
	instruction := roleInstructions[role]
	if instruction == "" {
		instruction = "Focus on the requested specialist role."
	}

	return fmt.Sprintf(`# Mission
You are an AI assistant running with GitHub MCP tools (repos, issues, pull_requests). You must not push code, create branches, open PRs, merge, or modify repository contents. Read and review only.

Repository: %s
PR: #%d
Fingerprint: %s

## Required reading
1. PR title/body and diff.
2. Changed files and relevant code context.

## Task
Provide exactly one specialist review comment using this heading:

%s

Role guidance: %s

### Output requirements
- Post one top-level PR review summary comment.
- Add inline review comments only when there is a clear actionable issue.
- Keep changes minimal; propose follow-up issues for large refactors.

### Footer marker
Include this marker in the top-level review body:

<!-- ai-daemon:pr-review v1; fingerprint=%s -->

### Output Plan
Post the PR review via GitHub MCP pull request review tools with inline comments where possible.

### STDOUT JSON (mandatory)
After posting the review, you MUST print exactly one JSON object to stdout (no other text before or after it) with the artifacts you created:

{"summary":"<one-line summary>","artifacts":[{"type":"pr_review","part_key":"review/%s/%s","github_id":"<review_id>","url":"<review_url>"}]}

Do NOT output anything else to stdout. Only the JSON object above.
`, repo, number, fingerprint, heading, instruction, fingerprint, agent, role)
}
