# Catalog Versioning

Prompts, skills, and guardrails have stable catalog identities and immutable
version rows. Normal edits publish a new current version by default, while
draft edits can be saved without affecting future agent runs.

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
that historical version into the editor and publish it as a new current version.

Every run stores the exact prompt, skill, and guardrail version ids that were
resolved for that run. The composed prompt is still stored on the trace as
before.

## Drafts And Publishing

The REST edit endpoints publish by default:

```http
PATCH /prompts/{id}
PATCH /skills/{id}
PATCH /guardrails/{id}
```

Send `"publish": false` to save a draft version instead. Drafts do not update
the live catalog asset and do not affect agents tracking the current version.
For now, each catalog asset can have only one open `draft` or `proposal`
version at a time. Publish or otherwise resolve the existing open version
before saving another draft for the same prompt, skill, or guardrail.

```json
{
  "content": "Updated prompt body",
  "publish": false
}
```

Direct API clients can create attributed inert proposal versions through the
same edit endpoints by setting `publish: false`, `state: "proposal"`, and
source metadata. The self-improvement workflow does not create catalog version
rows during analysis; it stores inert proposal bundle items and creates catalog
versions only when a human publishes the bundle.

```json
{
  "content": "Updated prompt body",
  "publish": false,
  "state": "proposal",
  "source_type": "feedback_recommendation",
  "source_ref": "rec_123",
  "author": "agents-assistant",
  "changelog": "Proposed from repeated review feedback"
}
```

Supported source types are `manual`, `feedback_recommendation`, and
`audit_recommendation` for API-created versions. Migration-created seed
versions use `migration`. Proposal versions are inert until a user explicitly
publishes them.

Publish a draft explicitly:

```http
POST /prompts/{id}/versions/{version_id}/publish
POST /skills/{id}/versions/{version_id}/publish
POST /guardrails/{id}/versions/{version_id}/publish
```

Publishing is guarded against stale drafts. If another version was published
after a draft was created, refresh from the current version before publishing
the older draft.

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

Use this before publishing shared catalog changes. A tracking reference to the
current version means the next publish will affect that agent or workspace on
its next run.

## Import And Export

YAML import/export includes catalog version histories. Live agents and
workspace guardrail references only store stable catalog asset references; they
do not store exact version pins.
