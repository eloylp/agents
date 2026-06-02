-- 042_self_improvement_analyst_prompt_v3_bundles: teach the built-in
-- self-improvement analyst to emit reactive multi-asset bundle changes.

INSERT INTO prompt_versions (
    id, prompt_id, version_number, state, description, content,
    source_type, source_ref, author, changelog, base_version_id, body_hash, created_at, published_at
)
SELECT
    'promptver_self_improvement_analyst_v3',
    p.id,
    COALESCE((SELECT MAX(version_number) FROM prompt_versions WHERE prompt_id = p.id), 0) + 1,
    'published',
    'Built-in analyst prompt for turning feedback evidence into reviewable recommendations.',
    v2.content || '

Bundle recommendations:
- Use type catalog_patch_bundle when one feedback event genuinely needs coordinated prompt, skill, or guardrail changes.
- Add changes when returning a bundle recommendation. Each change must include operation update_existing or create_new, asset_type prompt, skill, or guardrail, and proposed_body.
- For update_existing changes include asset_id and base_version_id from supplied catalog context. For create_new changes include proposed_ref, proposed_name, proposed_scope, duplicate_risk, and rationale.
- Use a single change when one catalog asset is sufficient. Use multiple changes only when complementary catalog edits are necessary.
- Never include an attributed asset just because it was present in the run.
- Prefer needs_user_input when the split between prompt, skill, and guardrail is ambiguous.
- Keep no_auto_apply_confirmed=true. Bundle creation is a human-triggered staging action and publish remains a separate human action.

Return one structured JSON recommendation with the existing single-target fields and, when type is catalog_patch_bundle, a changes array of proposed catalog changes. Preserve the existing single-target behavior for simple recommendations.',
    'manual',
    '',
    'system',
    'Add reactive multi-asset proposal bundle structured output guidance',
    'promptver_self_improvement_analyst_v2',
    'da6f0c510ecbb3b9acefe3a1b58382e53f4aaa2d4bc16a1792c21c76422fc73b',
    datetime('now'),
    datetime('now')
FROM prompts p
JOIN prompt_versions v2 ON v2.id = 'promptver_self_improvement_analyst_v2'
WHERE p.ref = 'prompt_self-improvement-analyst'
  AND NOT EXISTS (
      SELECT 1 FROM prompt_versions
      WHERE id = 'promptver_self_improvement_analyst_v3'
  );

UPDATE prompts
SET
    description = (SELECT description FROM prompt_versions WHERE id = 'promptver_self_improvement_analyst_v3'),
    content = (SELECT content FROM prompt_versions WHERE id = 'promptver_self_improvement_analyst_v3'),
    current_version_id = 'promptver_self_improvement_analyst_v3',
    updated_at = datetime('now')
WHERE ref = 'prompt_self-improvement-analyst'
  AND current_version_id = 'promptver_self_improvement_analyst_v2'
  AND EXISTS (
      SELECT 1 FROM prompt_versions
      WHERE id = 'promptver_self_improvement_analyst_v3'
  );
