# Run Attribution Metadata

Agent runs persist a compact attribution snapshot so later feedback can be
linked back to the run that produced a PR, comment, or commit.

The private snapshot is stored in `run_attributions` and includes workspace,
repository, issue or PR number, event id, event queue row, trace span id,
agent/backend identity, prompt reference, nullable catalog version ids, head SHA,
branch, and creation time. Public metadata is a smaller lookup token and must
never include prompt bodies, secrets, token values, trace content, event queue
ids, or other sensitive runtime details.

When an agent creates or updates GitHub content, the runtime prompt asks it to
include a hidden HTML comment:

```markdown
<!-- agents-run: {"v":1,"instance_id":"prod-a","workspace":"default","repo":"owner/repo","number":42,"span_id":"...","agent_id":"...","agent_name":"coder","sig":"..."} -->
```

For commits, agents should add trailers instead:

```text
Agents-Attribution: <base64url-json>
Agents-Run: <span_id>
Agents-Agent: <agent_name>
```

The daemon does not rewrite PR bodies, comments, or commits to add this data.
Metadata is supplied through the agent workflow contract so repository writes
remain owned by the normal agent action path.

When `AGENTS_ATTRIBUTION_SIGNING_SECRET` is configured, the public metadata is
signed by the daemon before the agent run starts. The AI only copies the
already-signed comment or trailer; it never calculates signatures. The
signature is an HMAC integrity check, not encryption. Changing the secret means
old signed metadata no longer verifies for exact attribution. `AGENTS_INSTANCE_ID`
should stay stable per daemon deployment, especially when multiple installations
can operate on the same repositories.

Exact resolver lookups are scoped to the query workspace. Inferred resolver
lookups also require repository, issue/PR number, and a non-zero event time; a
zero event time fails closed instead of searching all historical snapshots.
Signed exact resolver lookups also require the signed repository and issue/PR
number to match the feedback location, so copied metadata from another item is
ignored.

The resolver uses three confidence values:

- `exact`: a valid hidden comment, commit trailer, or stored artifact ancestry
  names a known span.
- `inferred`: repo, issue/PR number, optional SHA, and time window match exactly
  one stored attribution snapshot.
- `unresolved`: no match exists, the exact span is unknown, or inference is
  ambiguous.

Each resolution also reports a `mode` describing how the span was found:

- `direct`: signed hidden comment in the feedback body.
- `commit_trailer`: `Agents-Attribution:` commit trailer in the same body.
- `commit_artifact`: push webhook captured a signed `Agents-Attribution`
  trailer for the commit SHA GitHub attached to a diff-line review comment.
- `artifact_comment`: the comment itself was stored as a signed artifact.
- `artifact_parent_comment`: parent comment via `in_reply_to_id` is a signed artifact.
- `artifact_review`: owning PR review via `pull_request_review_id` is a signed artifact.
- `artifact_pr_context`: single matching artifact in the same PR / file / commit context.
- `inferred`: time-window match in `run_attributions`.
- `unresolved`: no ownership found.

## Artifact reverse-index

Every incoming `issue_comment`, `pull_request_review`,
`pull_request_review_comment`, and `push` webhook delivery is scanned for valid
signed agent metadata, regardless of whether it contains `/agents improve`.
Valid artifacts are stored in the `run_attribution_artifacts` table as a narrow
reverse index: GitHub object identity (comment id, review id, review comment id,
or commit SHA) → span id.

When a maintainer posts an `/agents improve` inline PR review comment:
1. The daemon checks the review comment's `commit_id` for a stored signed commit
   artifact captured from a prior push webhook.
2. If not found, it walks the `in_reply_to_id` chain to find the parent agent
   inline comment.
3. It then checks `pull_request_review_id` to see if the owning PR review
   carried signed metadata.
4. It checks the comment's own `id` for a stored artifact.
5. As a conservative fallback, it looks for artifacts on the same PR/file/commit
   that have exactly one candidate.
6. Only if none of the above succeeds does it fall back to time-window inference,
   which never attributes feedback to internal analyst agent runs.

If a diff-line review comment references a commit SHA but no signed commit
artifact exists, attribution remains unresolved with a diagnostic that the
commented commit has no signed agent attribution, unless stronger parent or
review ancestry resolves first.

Copied signed metadata from another repo, PR, or instance is rejected. If
multiple artifact candidates exist for the same PR context without stronger
ancestry evidence, the resolution is ambiguous/unresolved rather than guessing.

If signing is enabled, unsigned, malformed, foreign-instance, or invalid
metadata is logged and ignored for exact attribution. Authorized feedback can
still be captured, but untrusted span, agent, prompt, skill, or guardrail fields
are not passed through as exact attribution.

Catalog version ids are nullable on deployments that do not yet have immutable
catalog versioning. Once versioned prompts, skills, and guardrails are available,
the same snapshot fields should carry exact version ids.
