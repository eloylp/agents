# Catalog Versioning

Prompts, skills, and guardrails have stable catalog identities and immutable
version rows. Normal edits publish a new current version by default, while
draft edits can be saved without affecting future agent runs.

## References

Agents and workspaces can either track the current published version or pin an
exact version.

```yaml
agents:
  - name: coder
    prompt_ref: coder
    skills:
      - go-testing

  - name: cautious-coder
    prompt_ref: coder
    prompt_version_id: promptver_...
    skills:
      - go-testing@2

workspaces:
  - name: default
    guardrails:
      - guardrail_name: security
      - guardrail_name: release-safety
        guardrail_version_id: guardrailver_...
```

Tracking references resolve to the asset's current published version at prompt
composition time. Exact pins keep using the selected immutable version until a
user updates the reference.

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

```json
{
  "content": "Updated prompt body",
  "publish": false
}
```

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

The response names each referencing agent or workspace and marks whether the
reference is tracking current or pinned exactly:

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
its next run; a pinned reference will not move until explicitly changed.

Upgrade exact pins from one version to another:

```http
POST /prompts/{id}/versions/{from_version_id}/rollout
POST /skills/{id}/versions/{from_version_id}/rollout
POST /guardrails/{id}/versions/{from_version_id}/rollout
```

```json
{
  "to_version_id": "promptver_current"
}
```

The rollout target must be a published version for the same prompt, skill, or
guardrail. Tracking references are left alone because they already follow the
current published version. The UI exposes the same action as "Upgrade N exact
pins to vX" after you inspect a version's live references, and warns when a
version has multiple live references.

## Import And Export

YAML import/export includes catalog version histories and exact agent skill,
prompt, and workspace guardrail pins. Imported histories preserve version IDs
so pinned references continue to resolve after a round trip.
