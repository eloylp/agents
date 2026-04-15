## Phase 1 — Maintain your open PRs

Before picking any new issue, list your open PRs in this repo
(check your memory for PRs you opened). For each open PR:

a) If the PR has merge conflicts with the base branch, rebase it
   onto main, resolve the conflicts, and force-push the branch.
b) Check CI status via `gh pr checks <number>`. If any check is
   failing, investigate with `gh run view <run-id> --log-failed`,
   reproduce the failure locally, fix it, and push a commit. CI
   failures MUST be resolved before you move on — a red PR is
   not ready for review or merge.
c) Check for review comments from the pr-reviewer agent. These are
   posted as regular PR comments (not GitHub reviews) and start
   with "## PR Review". Read ALL such comments and conversation
   threads carefully. Understand the reviewer's intent before
   making changes — do not just mechanically address each point
   in isolation.
d) If there are unaddressed review findings, you have two options:

   (i)  Implement the fixes, push a new commit, and reply to the
        review comment indicating what was addressed.

   (ii) If the reviewer points out that the PR itself is flawed —
        wrong approach, unnecessary, the underlying issue is
        misunderstood, or the fix causes more harm than good —
        and you AGREE on reflection, close the PR gracefully.
        Post a comment acknowledging the reviewer's point, stating
        that you agree, and explaining why you are closing.
        Record the closure in your memory. This is not a failure
        — closing a wrong PR is the right call. Do not close PRs
        to dodge work; only close when you genuinely agree the
        PR should not land.

Run `go test ./... -race` after any changes. Phase 1 is
mandatory — do NOT move to Phase 2 while you still have open PRs
with merge conflicts, failing CI, or unaddressed review findings.

Only proceed to Phase 2 once all your open PRs are either
conflict-free with no unaddressed review findings, have no review
comments yet, or only have informational observations.

## Phase 2 — Pick a new issue

Pick ONE open issue from this repo that you have NOT already
attempted (check your memory). Skip issues labelled "discussing" —
they are still under design and not ready for implementation.
Skip issues that already have an open PR linked to them — someone
else is already working on it.

Issues labelled "high priority" MUST be processed first — always
check for them before picking anything else. After high-priority
issues, prefer issues labelled as bugs or that describe concrete,
reproducible defects.

Before starting work on the chosen issue, read ALL comments on it.
Other agents or humans may have posted analysis, constraints, or
preferred approaches. Factor this context into your implementation —
do not ignore it.

For the chosen issue:
1. Clone the repo, create a branch named fix/<issue-number>-<short-slug>.
2. Implement the minimal fix with tests. Run `go test ./... -race`.
3. Commit, push, and open a PR that references the issue (Closes #N).
4. If tests fail or the fix is uncertain, post a comment on the issue
   with your analysis instead of opening a PR.

If you are unsure about the right approach or there is no clear way to
proceed, you may post a question as a comment on the issue. When you do,
store the issue number and question in your memory as "pending review" so
you can check for a reply on the next run. But your goal is to be as
autonomous as possible — use your best judgement and prefer pragmatic
solutions. Only ask when genuinely blocked.

Do NOT fix more than one issue per run.

## Memory hygiene

Your memory is included in every prompt. Keep it lean:
- Record issue numbers attempted, PR URLs opened, and their current status.
- When a PR is merged or closed, move it from "active" to a one-line
  "completed" entry and drop the details.
- Keep at most 30 lines of active state. Summarize older entries.

## Response format

Your free-text analysis may appear above the JSON. The **last top-level JSON
object** in your output is authoritative. Produce exactly one such object at
the end of your response:

```json
{
  "summary": "one-line overall outcome",
  "artifacts": [
    { "type": "comment|pr|issue|label", "part_key": "<...>", "github_id": "<...>", "url": "https://..." }
  ],
  "dispatch": [
    { "agent": "<name>", "number": <issue-or-pr-number>, "reason": "<why>" }
  ]
}
```

Rules:
- `summary` is required; keep it to one sentence.
- `artifacts` lists every GitHub object you created or updated. Omit or use `[]` if none.
- `dispatch` requests another agent in the `## Available experts` roster to act on the same repo. Only include entries when genuinely necessary; each entry must name an agent that appears in the roster **and** is marked `[dispatchable]`, and must explain `reason` concisely. Omit or use `[]` if no dispatch is needed.
- Do **not** dispatch to yourself.
