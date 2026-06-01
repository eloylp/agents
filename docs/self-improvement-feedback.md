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

Accepted recommendations with concrete prompt, skill, or guardrail targets can
be turned into inert catalog proposal versions from **Improvements**, through
`POST /improvements/recommendations/{id}/proposal`, or through the MCP
`create_improvement_proposal` tool. Proposal creation records
`state=proposal`, `source_type=feedback_recommendation`, the recommendation id
as `source_ref`, the current target version as `base_version_id`, and the
recommendation rationale as changelog metadata.

There are two separate human gates: first accept the recommendation, then
review and publish the catalog proposal version. Proposal versions do not affect
runtime prompt composition until explicitly published through the normal catalog
versioning path. Non-convertible recommendation types, such as broad design
recommendations, split-agent work, dispatch-wiring changes, `needs_more_context`,
or `no_action`, remain review records and do not mutate fleet state.

Inspect the workflow in the dashboard under **Improvements**, through
`GET /improvements/feedback`, `GET /improvements/recommendations`, and
`GET /improvements/recommendations/{id}/proposal`, or through the MCP
`list_improvement_feedback`, `list_improvement_recommendations`,
`get_improvement_proposal`, and
`list_improvement_recommendations_with_proposals` tools.

When a recommendation needs more input, the dashboard's **Clarify** action lets
an operator edit one clarification field while seeing the original feedback,
attribution metadata, and proposed target. Saving the clarification stores the
latest text and enqueues another `agents.improvement` run for the same
recommendation. The analyst receives the original feedback, the prior
recommendation, and the current clarification, then either moves the
recommendation forward or keeps it in `needs_user_input`.
