-- 044_self_improvement_analyst_prompt_v4_code_examples: make the built-in
-- analyst prefer concrete code examples when they reduce implementation
-- ambiguity.

INSERT INTO prompt_versions (
    id, prompt_id, version_number, state, description, content,
    source_type, source_ref, author, changelog, base_version_id, body_hash, created_at, published_at
)
SELECT
    'promptver_self_improvement_analyst_v4',
    p.id,
    COALESCE((SELECT MAX(version_number) FROM prompt_versions WHERE prompt_id = p.id), 0) + 1,
    'published',
    'Built-in analyst prompt for turning feedback evidence into reviewable recommendations.',
    v3.content || '

Implementation-guidance examples:
- When a recommendation changes coding or implementation guidance, prefer short code examples over abstract natural language if examples would reduce ambiguity.
- Use compact before/after snippets for API shapes, function signatures, handler structure, file splitting, data modeling, or similarly concrete coding patterns.
- Keep examples minimal and directly tied to the proposed rule. Do not add examples when the rule is already unambiguous, when examples would bloat the catalog, or when reusable example-heavy guidance belongs in a skill.',
    'manual',
    '',
    'system',
    'Prefer concrete code examples for implementation guidance when they reduce ambiguity',
    'promptver_self_improvement_analyst_v3',
    '6983336d3f5a1a518ba24e8fc7eb524c73d5a8984be4758f29cc6c0883dc55a0',
    datetime('now'),
    datetime('now')
FROM prompts p
JOIN prompt_versions v3 ON v3.id = 'promptver_self_improvement_analyst_v3'
WHERE p.ref = 'prompt_self-improvement-analyst'
  AND NOT EXISTS (
      SELECT 1 FROM prompt_versions
      WHERE id = 'promptver_self_improvement_analyst_v4'
  );

UPDATE prompts
SET
    description = (SELECT description FROM prompt_versions WHERE id = 'promptver_self_improvement_analyst_v4'),
    content = (SELECT content FROM prompt_versions WHERE id = 'promptver_self_improvement_analyst_v4'),
    current_version_id = 'promptver_self_improvement_analyst_v4',
    updated_at = datetime('now')
WHERE ref = 'prompt_self-improvement-analyst'
  AND current_version_id = 'promptver_self_improvement_analyst_v3'
  AND EXISTS (
      SELECT 1 FROM prompt_versions
      WHERE id = 'promptver_self_improvement_analyst_v4'
  );
