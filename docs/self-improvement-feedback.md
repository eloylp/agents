# Self-Improvement Feedback

The daemon can capture maintainer review lessons from GitHub comments marked
with `/agents improve`, turn authorized feedback into a durable proposal
candidate, and present that candidate for human review. The flow stays gated:
operators can clarify or reject proposal candidates, but candidates do not
publish catalog changes or mutate runtime behavior.

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

Ready proposal candidates with concrete prompt, skill, or guardrail changes
automatically carry an editable proposal bundle. There is no separate accept
gate before bundle creation: the analyst output is already the proposal.
Humans either clarify, reject, or inspect the bundle. A bundle stores editable
staging items only; it does not create prompt, skill, or guardrail version rows
during analysis and it remains ignored by runtime prompt composition.

Non-convertible recommendation types, such as broad design recommendations,
split-agent work, dispatch-wiring changes, `needs_more_context`, or `no_action`,
remain review records and do not mutate fleet state.

Bundle items support updating existing prompts, skills, and guardrails, and
proposing new catalog assets. Before publish, operators can edit staged bodies,
reject items with an optional reason, or resolve create-new items as already
covered by an existing catalog asset. That link-existing decision does not
attach the selected asset to any agent; it only records that the proposed new
asset should not be created. Rejecting a proposal stores an optional
proposal-level decision reason; rejecting the only item in a bundle also
terminally rejects the proposal. A catalog asset can have only one open
self-improvement draft at a time: pending bundles that already target a prompt,
skill, guardrail, or create-new ref block additional drafts for the same
catalog item until the first bundle is published, rejected, linked, or
discarded. `Publish Bundle` is atomic for accepted publishable items: stale
base versions, duplicate new refs, invalid items, open-draft conflicts, or
write failures roll back the whole publish transaction. Link-existing and
rejected decisions are preserved as review evidence without creating catalog
versions.

Failed analysis is not a final human decision. A failed initial analysis can be
queued again from the feedback event, and a failed clarification run can be
retried through the same clarification endpoint with the latest stored
clarification body. Rejected, published, resolved, and discarded records are
terminal history.

Inspect the workflow in the dashboard under **Improvements**, through
`GET /improvements/feedback`, `POST /improvements/feedback/{id}/analyze`,
`GET /improvements/recommendations`,
`POST /improvements/recommendations/{id}/status`,
`POST/PATCH /improvements/recommendations/{id}/clarification`,
`GET /improvements/recommendations/{id}/proposal-bundle`, and the
`/improvements/proposal-bundles/{id}/...` item reject/link/edit/publish/discard endpoints, or
through the MCP `list_improvement_feedback`,
`list_improvement_recommendations`,
`analyze_improvement_feedback`,
`update_improvement_recommendation_status`,
`clarify_improvement_recommendation`,
`get_improvement_proposal_bundle`, `edit_improvement_proposal_bundle_item`,
`reject_improvement_proposal_bundle_item`,
`link_improvement_proposal_bundle_item`,
`publish_improvement_proposal_bundle`,
`discard_improvement_proposal_bundle`,
`list_improvement_recommendations_with_bundles` tools.

Improvement listings are global by default. The stored rows still retain
`workspace_id` as attribution and catalog-scope provenance, and API clients may
pass `workspace` to narrow diagnostic views.

Single-target proposals are for simple one-asset edits. Reactive multi-asset
bundles are feedback-driven and keep coordinated changes together. Proactive
catalog audits are a separate workflow that reviews the catalog without a
specific feedback event.

The v1 self-improvement loop has no assistant preference memory. The analyst's
decision process is intentionally prompt-led: feedback, attribution, current
catalog context, and optional maintainer clarification are the only analysis
inputs. If recommendation behavior should change, update the built-in analyst
prompt or the affected prompt/skill/guardrail explicitly so the decision logic
stays inspectable and versioned.

When a recommendation needs more input, the dashboard's **Clarify** action lets
an operator edit one clarification field while seeing the original feedback,
attribution metadata, and proposed target. Saving the clarification stores the
latest text and enqueues another `agents.improvement` run for the same
recommendation. The analyst receives the original feedback, the prior
recommendation, and the current clarification, then either moves the
recommendation forward or keeps it in `needs_user_input`.
