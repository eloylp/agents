-- 053_self_improvement_remove_legacy_fields: remove retired self-improvement
-- fields and item states from the live schema. Rollout decisions now live on
-- proposal bundle items, and bundle items are accepted/rejected/resolved
-- directly rather than passing through a pending decision state.

ALTER TABLE self_improvement_recommendations DROP COLUMN suggested_rollout_scope;

UPDATE self_improvement_proposal_bundle_items
SET decision = 'accepted'
WHERE decision = 'pending';

PRAGMA foreign_keys = OFF;

CREATE TABLE self_improvement_proposal_bundle_items_new (
    id                    TEXT PRIMARY KEY,
    bundle_id             TEXT NOT NULL REFERENCES self_improvement_proposal_bundles(id) ON DELETE CASCADE,
    operation             TEXT NOT NULL CHECK (operation IN ('update_existing', 'create_new')),
    asset_type            TEXT NOT NULL CHECK (asset_type IN ('prompt', 'skill', 'guardrail')),
    asset_id              TEXT NOT NULL DEFAULT '',
    base_version_id       TEXT NOT NULL DEFAULT '',
    proposed_ref          TEXT NOT NULL DEFAULT '',
    proposed_name         TEXT NOT NULL DEFAULT '',
    proposed_scope        TEXT NOT NULL DEFAULT '',
    proposed_body         TEXT NOT NULL DEFAULT '',
    proposed_description  TEXT NOT NULL DEFAULT '',
    proposed_enabled      INTEGER NOT NULL DEFAULT 1,
    proposed_position     INTEGER NOT NULL DEFAULT 100,
    analyst_proposed_body TEXT NOT NULL DEFAULT '',
    duplicate_risk        TEXT NOT NULL DEFAULT '',
    rationale             TEXT NOT NULL DEFAULT '',
    decision              TEXT NOT NULL DEFAULT 'accepted' CHECK (decision IN ('accepted', 'rejected', 'linked_existing', 'published', 'discarded')),
    decision_reason       TEXT NOT NULL DEFAULT '',
    published_version_id  TEXT NOT NULL DEFAULT '',
    created_at            TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at            TEXT NOT NULL DEFAULT (datetime('now'))
);

INSERT INTO self_improvement_proposal_bundle_items_new (
    id, bundle_id, operation, asset_type, asset_id, base_version_id,
    proposed_ref, proposed_name, proposed_scope, proposed_body,
    proposed_description, proposed_enabled, proposed_position,
    analyst_proposed_body, duplicate_risk, rationale, decision,
    decision_reason, published_version_id, created_at, updated_at
)
SELECT
    id, bundle_id, operation, asset_type, asset_id, base_version_id,
    proposed_ref, proposed_name, proposed_scope, proposed_body,
    proposed_description, proposed_enabled, proposed_position,
    analyst_proposed_body, duplicate_risk, rationale, decision,
    decision_reason, published_version_id, created_at, updated_at
FROM self_improvement_proposal_bundle_items;

DROP TABLE self_improvement_proposal_bundle_items;

ALTER TABLE self_improvement_proposal_bundle_items_new
RENAME TO self_improvement_proposal_bundle_items;

CREATE INDEX IF NOT EXISTS idx_self_improvement_proposal_bundle_items_bundle
    ON self_improvement_proposal_bundle_items(bundle_id, asset_type, id);

PRAGMA foreign_key_check;
PRAGMA foreign_keys = ON;
