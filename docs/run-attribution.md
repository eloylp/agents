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

The resolver uses three outcomes:

- `exact`: a valid hidden comment or commit trailer names a known span.
- `inferred`: repo, issue/PR number, optional SHA, and time window match exactly
  one stored attribution snapshot.
- `unresolved`: no match exists, the exact span is unknown, or inference is
  ambiguous.

If signing is enabled, unsigned, malformed, foreign-instance, or invalid
metadata is logged and ignored for exact attribution. Authorized feedback can
still be captured, but untrusted span, agent, prompt, skill, or guardrail fields
are not passed through as exact attribution.

Catalog version ids are nullable on deployments that do not yet have immutable
catalog versioning. Once versioned prompts, skills, and guardrails are available,
the same snapshot fields should carry exact version ids.
