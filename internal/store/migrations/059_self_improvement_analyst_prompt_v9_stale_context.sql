-- 059_self_improvement_analyst_prompt_v9_stale_context: explain how the
-- analyst should use stale attributed catalog versions alongside current
-- versions when feedback points at an older run.

INSERT INTO prompt_versions (
    id, prompt_id, version_number, state, description, content,
    source_type, source_ref, author, changelog, base_version_id, body_hash, created_at, published_at
)
SELECT
    'promptver_self_improvement_analyst_v9',
    p.id,
    COALESCE((SELECT MAX(version_number) FROM prompt_versions WHERE prompt_id = p.id), 0) + 1,
    'published',
    v8.description,
    REPLACE(
        v8.content,
        'Catalog context is attribution-only. Full catalog bodies are supplied only for run-attributed prompt, skill, and guardrail versions. If no catalog bodies are supplied, or if the supplied bodies are insufficient to identify a safe target and complete change, return status=needs_user_input instead of scanning or guessing from the wider catalog.',
        'Catalog context is attribution-only. Full catalog bodies are supplied only for run-attributed prompt, skill, and guardrail versions, plus the current version of the same catalog asset when the attributed version is stale. Treat relation=attributed entries as historical evidence of what produced the feedback. Treat relation=current entries as the safe base for any proposed update. If an attributed version is marked unavailable, explain that the historical body is missing and return needs_user_input unless the supplied current asset context and maintainer feedback are enough to make a precise, non-speculative recommendation. Never scan or guess from the wider catalog.'
    ),
    'manual',
    '',
    'system',
    'Teach analyst to compare stale attributed versions with current catalog versions',
    'promptver_self_improvement_analyst_v8',
    '4fba6f0e469213a57addd04c914abf844c159779cc95c58052d8b0010f73d7e9',
    datetime('now'),
    datetime('now')
FROM prompts p
JOIN prompt_versions v8 ON v8.id = 'promptver_self_improvement_analyst_v8'
WHERE p.ref = 'prompt_self-improvement-analyst'
  AND NOT EXISTS (
      SELECT 1 FROM prompt_versions
      WHERE id = 'promptver_self_improvement_analyst_v9'
  );

UPDATE prompts
SET
    description = (SELECT description FROM prompt_versions WHERE id = 'promptver_self_improvement_analyst_v9'),
    content = (SELECT content FROM prompt_versions WHERE id = 'promptver_self_improvement_analyst_v9'),
    current_version_id = 'promptver_self_improvement_analyst_v9',
    updated_at = datetime('now')
WHERE ref = 'prompt_self-improvement-analyst'
  AND current_version_id = 'promptver_self_improvement_analyst_v8'
  AND EXISTS (
      SELECT 1 FROM prompt_versions
      WHERE id = 'promptver_self_improvement_analyst_v9'
  );
