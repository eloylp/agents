-- 054_self_improvement_analyst_prompt_v8_schema_alignment: remove a retired
-- structured-output field from the built-in analyst prompt so the prose
-- contract matches the enforced JSON schema.

INSERT INTO prompt_versions (
    id, prompt_id, version_number, state, description, content,
    source_type, source_ref, author, changelog, base_version_id, body_hash, created_at, published_at
)
SELECT
    'promptver_self_improvement_analyst_v8',
    p.id,
    COALESCE((SELECT MAX(version_number) FROM prompt_versions WHERE prompt_id = p.id), 0) + 1,
    'published',
    v7.description,
    REPLACE(
        v7.content,
        'Return one structured JSON recommendation with: type, status, confidence, risk, finding, normalized_lesson, rationale, evidence_feedback_ids, evidence_source_urls, attribution_confidence, target_asset_type, target_asset_id, target_base_version_id, proposed_patch, proposed_new_body, suggested_rollout_scope. Use only machine-owned statuses recommended or needs_user_input; human decision states are not allowed in analyst output.',
        'Return one structured JSON recommendation with: type, status, confidence, risk, finding, normalized_lesson, rationale, evidence_feedback_ids, evidence_source_urls, attribution_confidence, target_asset_type, target_asset_id, target_base_version_id, proposed_patch, proposed_new_body, changes, and no_auto_apply_confirmed. Use only machine-owned statuses recommended or needs_user_input; human decision states are not allowed in analyst output.'
    ),
    'manual',
    '',
    'system',
    'Align self-improvement analyst prompt with structured output schema',
    'promptver_self_improvement_analyst_v7',
    '7322b57911b6eb9634fb12552b6c5ed3096307637ac6fd5e009b6f88007014ff',
    datetime('now'),
    datetime('now')
FROM prompts p
JOIN prompt_versions v7 ON v7.id = 'promptver_self_improvement_analyst_v7'
WHERE p.ref = 'prompt_self-improvement-analyst'
  AND NOT EXISTS (
      SELECT 1 FROM prompt_versions
      WHERE id = 'promptver_self_improvement_analyst_v8'
  );

UPDATE prompts
SET
    description = (SELECT description FROM prompt_versions WHERE id = 'promptver_self_improvement_analyst_v8'),
    content = (SELECT content FROM prompt_versions WHERE id = 'promptver_self_improvement_analyst_v8'),
    current_version_id = 'promptver_self_improvement_analyst_v8',
    updated_at = datetime('now')
WHERE ref = 'prompt_self-improvement-analyst'
  AND current_version_id = 'promptver_self_improvement_analyst_v7'
  AND EXISTS (
      SELECT 1 FROM prompt_versions
      WHERE id = 'promptver_self_improvement_analyst_v8'
  );
