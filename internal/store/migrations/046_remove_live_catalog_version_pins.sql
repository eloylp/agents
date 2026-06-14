-- 046_remove_live_catalog_version_pins: live fleet entities always track the
-- current published catalog asset. Immutable version history remains in the
-- *_versions tables and resolved runtime versions remain on traces/attribution.

ALTER TABLE agents DROP COLUMN prompt_version_id;
ALTER TABLE agent_skills DROP COLUMN skill_version_id;
ALTER TABLE workspace_guardrails DROP COLUMN guardrail_version_id;
