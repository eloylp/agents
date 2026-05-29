# Run Attribution Metadata

Agent runs persist a compact attribution snapshot so later feedback can be
linked back to the run that produced a PR, comment, or commit.

The snapshot is stored in `run_attributions` and includes workspace, repository,
issue or PR number, event id, event queue row, trace span id, agent/backend
identity, prompt reference, nullable catalog version ids, head SHA, branch, and
creation time. Public metadata must never include prompt bodies, secrets, token
values, trace content, or other sensitive runtime details.

When an agent creates or updates GitHub content, the runtime prompt asks it to
include a hidden HTML comment:

```markdown
<!-- agents-run: {"workspace":"default","span_id":"...","agent_id":"..."} -->
```

For commits, agents should add trailers instead:

```text
Agents-Run: <span_id>
Agents-Agent: <agent_name>
```

The daemon does not rewrite PR bodies, comments, or commits to add this data.
Metadata is supplied through the agent workflow contract so repository writes
remain owned by the normal agent action path.

The resolver uses three outcomes:

- `exact`: a hidden comment or commit trailer names a known span.
- `inferred`: repo, issue/PR number, optional SHA, and time window match exactly
  one stored attribution snapshot.
- `unresolved`: no match exists, the exact span is unknown, or inference is
  ambiguous.

Catalog version ids are nullable on deployments that do not yet have immutable
catalog versioning. Once versioned prompts, skills, and guardrails are available,
the same snapshot fields should carry exact version ids.
