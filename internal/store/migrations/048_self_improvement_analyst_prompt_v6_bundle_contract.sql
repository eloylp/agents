-- 048_self_improvement_analyst_prompt_v6_bundle_contract: make ready
-- self-improvement catalog proposals consistently bundle-backed.

INSERT INTO prompt_versions (
    id, prompt_id, version_number, state, description, content,
    source_type, source_ref, author, changelog, base_version_id, body_hash, created_at, published_at
)
SELECT
    'promptver_self_improvement_analyst_v6',
    p.id,
    COALESCE((SELECT MAX(version_number) FROM prompt_versions WHERE prompt_id = p.id), 0) + 1,
    'published',
    v5.description,
    v5.content || '

Editable proposal contract:
- Every status=recommended catalog-changing result must be directly reviewable in the proposal bundle UI.
- Use type catalog_patch_bundle for every ready catalog change, including simple one-asset changes.
- Populate changes with at least one editable item. Do not leave changes empty for status=recommended catalog changes.
- For update_existing changes, include operation=update_existing, asset_type, asset_id, base_version_id, proposed_body, and rationale. proposed_body must be the full replacement body for that catalog asset, not a prose patch description.
- For create_new changes, include operation=create_new, asset_type, proposed_ref, proposed_name, proposed_scope, proposed_body, duplicate_risk, and rationale.
- Keep proposed_patch only as explanatory metadata. Never rely on proposed_patch instead of changes[].proposed_body.
- If the supplied catalog context is index-only or otherwise insufficient to write full editable proposed_body values, return status=needs_user_input and explain what catalog body or target clarification is missing.
- Preserve no_auto_apply_confirmed=true. The system stages bundles for human review; it must not apply or publish catalog changes itself.',
    'manual',
    '',
    'system',
    'Require ready self-improvement catalog proposals to be bundle-backed and editable',
    'promptver_self_improvement_analyst_v5',
    '8634070c00007f6dffcdf760ec112141d06568e538c2fa7b973b4ddac24fb171',
    datetime('now'),
    datetime('now')
FROM prompts p
JOIN prompt_versions v5 ON v5.id = 'promptver_self_improvement_analyst_v5'
WHERE p.ref = 'prompt_self-improvement-analyst'
  AND NOT EXISTS (
      SELECT 1 FROM prompt_versions
      WHERE id = 'promptver_self_improvement_analyst_v6'
  );

UPDATE prompts
SET
    description = (SELECT description FROM prompt_versions WHERE id = 'promptver_self_improvement_analyst_v6'),
    content = (SELECT content FROM prompt_versions WHERE id = 'promptver_self_improvement_analyst_v6'),
    current_version_id = 'promptver_self_improvement_analyst_v6',
    updated_at = datetime('now')
WHERE ref = 'prompt_self-improvement-analyst'
  AND current_version_id = 'promptver_self_improvement_analyst_v5'
  AND EXISTS (
      SELECT 1 FROM prompt_versions
      WHERE id = 'promptver_self_improvement_analyst_v6'
  );
