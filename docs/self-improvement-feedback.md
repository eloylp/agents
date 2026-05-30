# Self-Improvement Feedback

The daemon can capture maintainer review lessons from GitHub comments marked
with `/agents improve`. This issue implements only deterministic ingestion and
inspection. It does not invoke AI, create recommendations, or publish catalog
changes.

Supported webhook sources:

- issue comments
- pull request review comments
- pull request reviews

The marker match is exact and case-sensitive. Fenced code blocks are ignored so
examples do not create feedback records accidentally.

Only trusted GitHub authors create actionable feedback. Configure them at
startup:

```bash
AGENTS_SELF_IMPROVEMENT_FEEDBACK_AUTHOR_ALLOWLIST=maintainer-login,agents-bot
```

When the allowlist is omitted, `GITHUB_ACTOR` is used if available. If no
trusted actor can be determined, marked comments are stored with
`status=ignored`.

Stored feedback keeps the raw comment as source of truth, plus GitHub source
metadata, repo/issue/PR/file context, author authorization, delivery ids, and
run attribution when it can be resolved. Exact attribution comes from the public
`agents-run` hidden metadata or commit trailers; otherwise the resolver may
infer from repo, PR/issue number, commit SHA, and time window. Unresolved
feedback is still stored.

Inspect feedback in the dashboard under **Improvements**, through
`GET /improvements/feedback`, or through the MCP `list_improvement_feedback`
tool. Later recommendation and proposal flows consume these records but remain
separate human-gated steps.
