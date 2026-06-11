# Catalog Versioning

Prompts, skills, and guardrails have stable catalog identities and immutable
published version rows. A normal edit creates a new published current version
immediately, so agents that track that catalog asset use the new content on
their next run.

## References

Agents and workspaces track the current published version of each referenced
catalog asset.

```yaml
agents:
  - name: coder
    prompt_ref: coder
    skills:
      - go-testing

workspaces:
  - name: default
    guardrails:
      - guardrail_name: security
      - guardrail_name: release-safety
```

Tracking references resolve to the asset's current published version at prompt
composition time. To return to older content, use the rollback action to copy
that historical version into the editor and save it as a new current published
version.

Every run stores the exact prompt, skill, and guardrail version ids that were
resolved for that run. The composed prompt is still stored on the trace as
before.

## Editing

The REST edit endpoints publish immediately:

```http
PATCH /prompts/{id}
PATCH /skills/{id}
PATCH /guardrails/{id}
```

Example:

```json
{
  "content": "Updated prompt body"
}
```

The MCP `update_prompt`, `update_skill`, and `update_guardrail` tools follow
the same rule. There is no catalog draft state and no explicit publish endpoint
for individual catalog versions.

The self-improvement workflow keeps proposed changes in proposal bundle tables
until a human finalizes the bundle. Finalizing a bundle atomically creates the
new published catalog versions for accepted publishable items.

## Reviewing Rollout Impact

List version history:

```http
GET /prompts/{id}/versions
GET /skills/{id}/versions
GET /guardrails/{id}/versions
```

List live references that resolve to a specific version:

```http
GET /prompts/{id}/versions/{version_id}/references
GET /skills/{id}/versions/{version_id}/references
GET /guardrails/{id}/versions/{version_id}/references
```

The response names each agent or workspace that currently resolves to that
version:

```json
[
  {
    "kind": "agent",
    "workspace_id": "default",
    "name": "coder",
    "reference": "prompt",
    "tracking": true
  }
]
```

Use this before saving shared catalog changes. A tracking reference to the
current version means the next edit will affect that agent or workspace on its
next run.

## Import And Export

YAML import/export includes catalog version histories. Live agents and
workspace guardrail references only store stable catalog asset references; they
do not store exact version pins.
