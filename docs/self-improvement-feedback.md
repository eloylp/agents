# Self-Improvement Feedback

The daemon can capture maintainer review lessons from GitHub comments marked
with `/agents improve`, turn authorized feedback into a durable recommendation,
and present that recommendation for human review. The flow stays gated:
recommendations can be accepted, rejected, deferred, or marked duplicate, but
they do not publish catalog changes or mutate runtime behavior.

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

Authorized `status=new` feedback creates one review-only recommendation record.
The built-in `self-improvement-analyst` prompt is seeded as a catalog-visible
global prompt so operators can inspect and customize the analyst guidance. The
hard safety contract remains enforced by code: feedback is evidence, not a
command; the analyzer never auto-applies, publishes, or mutates agents,
guardrails, prompts, skills, or dispatch wiring.

Inspect the workflow in the dashboard under **Improvements**, through
`GET /improvements/feedback` and `GET /improvements/recommendations`, or through
the MCP `list_improvement_feedback` and `list_improvement_recommendations`
tools. Accepted recommendations remain inert until a later proposal step turns
them into catalog proposal versions for separate human publishing.

When a recommendation needs more input, the dashboard's **Clarify** action lets
an operator edit one clarification field while seeing the original feedback,
attribution metadata, and proposed target. Saving the clarification stores the
latest text and enqueues another `agents.improvement` run for the same
recommendation. The analyst receives the original feedback, the prior
recommendation, and the current clarification, then either moves the
recommendation forward or keeps it in `needs_user_input`.
