-- 045_self_improvement_analyst_prompt_v5_intelligence_language: align the
-- built-in analyst prompt with the Intelligence catalog naming used in the UI.

INSERT INTO prompt_versions (
    id, prompt_id, version_number, state, description, content,
    source_type, source_ref, author, changelog, base_version_id, body_hash, created_at, published_at
)
SELECT
    'promptver_self_improvement_analyst_v5',
    p.id,
    COALESCE((SELECT MAX(version_number) FROM prompt_versions WHERE prompt_id = p.id), 0) + 1,
    'published',
    v4.description,
    REPLACE(v4.content, 'knowledge cluster', 'intelligence cluster'),
    'manual',
    '',
    'system',
    'Align built-in analyst prompt terminology with the Intelligence catalog',
    'promptver_self_improvement_analyst_v4',
    'c63d8a96b5048067a55863b1e228b19a134c10edcf784c7d6423a81cfeeb8605',
    datetime('now'),
    datetime('now')
FROM prompts p
JOIN prompt_versions v4 ON v4.id = 'promptver_self_improvement_analyst_v4'
WHERE p.ref = 'prompt_self-improvement-analyst'
  AND NOT EXISTS (
      SELECT 1 FROM prompt_versions
      WHERE id = 'promptver_self_improvement_analyst_v5'
  );

UPDATE prompts
SET
    description = (SELECT description FROM prompt_versions WHERE id = 'promptver_self_improvement_analyst_v5'),
    content = (SELECT content FROM prompt_versions WHERE id = 'promptver_self_improvement_analyst_v5'),
    current_version_id = 'promptver_self_improvement_analyst_v5',
    updated_at = datetime('now')
WHERE ref = 'prompt_self-improvement-analyst'
  AND current_version_id = 'promptver_self_improvement_analyst_v4'
  AND EXISTS (
      SELECT 1 FROM prompt_versions
      WHERE id = 'promptver_self_improvement_analyst_v5'
  );
