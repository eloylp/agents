-- Phase 12: a positive-direction built-in guardrail telling agents to use the
-- GitHub MCP tools for repository interactions (read PRs, issues, comments,
-- files; post reviews, comments, labels). The seeded `security` guardrail
-- fences network access to the GitHub MCP tools but does not direct the
-- agent toward them. That distinction matters most for less-capable local
-- models routed through the Anthropic-OpenAI proxy: Qwen-class models are
-- more conservative about reaching for tools than hosted Claude, and an
-- explicit reminder shifts them from "guess" to "fetch and act".
--
-- Position 10 puts it immediately after the security fence (position 0) and
-- before any operator-added guardrails (default position 100). Operators
-- with a hosted-Claude-only fleet may disable it, hosted Claude reaches for
-- tools without prompting and the extra prompt budget is a small loss.
INSERT INTO guardrails (name, description, content, default_content, is_builtin, position)
WITH text(t) AS (VALUES ('## Repository interaction

To read or write repository state, use the GitHub MCP tools the daemon has registered on your CLI (issues, pull requests, comments, reviews, labels, files, branches, workflow runs). The trigger payload only carries a minimal envelope (event kind, repo, number, actor, the immediate trigger content). Anything else, the surrounding issue body, the PR description and diff, prior comments on the thread, the linked issue, file contents, CI status, must be fetched through those tools when the task calls for it. Do not assume the daemon pre-loaded what you need.

Examples of reading: fetching a pull request''s body and diff before reviewing it; listing the prior comments on an issue before replying; looking up the linked issue referenced in a PR description; checking workflow run status before merging.

Examples of writing (when permitted by your other guardrails and flags): posting reviews and comments, applying or removing labels, opening pull requests, requesting reviewers.

This guidance applies to every agent in the fleet. It is especially load-bearing for agents routed through local OpenAI-compatible models, which are more conservative about reaching for tools than hosted Claude. An explicit reminder helps them act rather than guess from a thin trigger envelope.'))
SELECT 'mcp-tool-usage',
       'Direct agents to use the GitHub MCP tools for repository interactions. Especially helpful for local models, which are more conservative about tool use than hosted Claude.',
       t, t, 1, 10
FROM text;
