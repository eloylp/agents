-- 052_self_improvement_analyst_prompt_v7_attribution_only: align the
-- built-in analyst prompt with attribution-only catalog context.

INSERT INTO prompt_versions (
    id, prompt_id, version_number, state, description, content,
    source_type, source_ref, author, changelog, base_version_id, body_hash, created_at, published_at
)
SELECT
    'promptver_self_improvement_analyst_v7',
    p.id,
    COALESCE((SELECT MAX(version_number) FROM prompt_versions WHERE prompt_id = p.id), 0) + 1,
    'published',
    v6.description,
    REPLACE(
        v6.content,
        'Catalog context is bounded. Full catalog bodies are supplied only for attributed targets. Compact catalog index entries are supplied for unresolved discovery. Prefer attributed targets when available, and use compact index entries only to identify likely follow-up targets.',
        'Catalog context is attribution-only. Full catalog bodies are supplied only for run-attributed prompt, skill, and guardrail versions. If no catalog bodies are supplied, or if the supplied bodies are insufficient to identify a safe target and complete change, return status=needs_user_input instead of scanning or guessing from the wider catalog.'
    ),
    'manual',
    '',
    'system',
    'Restrict self-improvement analyst catalog context to attributed assets',
    'promptver_self_improvement_analyst_v6',
    '4b601d800af7e0b000ce4c6e5e4d0cf51060bfa12e5af733349c4ed74fab9509',
    datetime('now'),
    datetime('now')
FROM prompts p
JOIN prompt_versions v6 ON v6.id = 'promptver_self_improvement_analyst_v6'
WHERE p.ref = 'prompt_self-improvement-analyst'
  AND NOT EXISTS (
      SELECT 1 FROM prompt_versions
      WHERE id = 'promptver_self_improvement_analyst_v7'
  );

UPDATE prompts
SET
    description = (SELECT description FROM prompt_versions WHERE id = 'promptver_self_improvement_analyst_v7'),
    content = (SELECT content FROM prompt_versions WHERE id = 'promptver_self_improvement_analyst_v7'),
    current_version_id = 'promptver_self_improvement_analyst_v7',
    updated_at = datetime('now')
WHERE ref = 'prompt_self-improvement-analyst'
  AND current_version_id = 'promptver_self_improvement_analyst_v6'
  AND EXISTS (
      SELECT 1 FROM prompt_versions
      WHERE id = 'promptver_self_improvement_analyst_v7'
  );
