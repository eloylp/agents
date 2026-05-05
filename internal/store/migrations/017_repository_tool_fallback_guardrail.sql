-- Phase 17: update the built-in repository interaction guardrail to
-- prefer GitHub MCP tools while explicitly allowing the authenticated gh CLI
-- as a fallback for complex local checkout/test/push workflows.
--
-- Operator edits are preserved: content is only updated when it still matches
-- the prior built-in default. default_content always moves forward so Reset to
-- default adopts the new guidance.
WITH text(t) AS (VALUES ('## Repository interaction

Prefer the GitHub MCP tools the daemon has registered on your CLI for repository state: issues, pull requests, comments, reviews, labels, files, branches, and workflow runs. The trigger payload only carries a minimal envelope (event kind, repo, number, actor, and immediate trigger content). Anything else, the surrounding issue body, PR description and diff, prior comments, linked issue, file contents, CI status, must be fetched live when the task calls for it. Do not assume the daemon pre-loaded what you need.

The authenticated GitHub CLI (`gh`) is also available as a fallback for complex repository workflows where MCP alone is not enough, especially when you need a safe local checkout/test loop before opening or updating a PR. Examples: cloning/fetching the repository, creating branches, checking auth with `gh auth status`, inspecting PRs/issues when an MCP tool is unavailable, pushing commits, or opening/updating PRs after local tests pass.

Use this order of preference:

1. Use GitHub MCP tools for normal GitHub reads and writes.
2. Use local shell tools inside the current repository checkout for code inspection, edits, builds, and tests.
3. Use `gh` only as the GitHub fallback when MCP is unavailable, insufficient for the workflow, or when a local checkout/test/push loop is required.

Do not make remote-only code patches for non-trivial implementation work. If you cannot establish a safe local checkout and run the relevant tests, report that blocker instead of opening a risky PR.

All other guardrails still apply: stay scoped to the current repository, do not read secrets, do not traverse unrelated repositories, and do not send data to arbitrary URLs.
'))
UPDATE guardrails
SET default_content = (SELECT t FROM text),
    content = CASE
      WHEN content = default_content THEN (SELECT t FROM text)
      ELSE content
    END,
    description = 'Prefer GitHub MCP tools for repository interactions; allow authenticated gh CLI fallback for complex local checkout/test/push workflows.',
    updated_at = datetime('now')
WHERE name = 'mcp-tool-usage' AND is_builtin = 1;
