-- 041_self_improvement_proposal_bundles: editable, inert staging records
-- for reactive multi-asset self-improvement catalog changes.

CREATE TABLE IF NOT EXISTS self_improvement_proposal_bundles (
    id                                 TEXT PRIMARY KEY,
    workspace_id                       TEXT NOT NULL DEFAULT 'default',
    recommendation_id                  TEXT NOT NULL UNIQUE REFERENCES self_improvement_recommendations(id) ON DELETE CASCADE,
    recommendation_updated_at_snapshot TEXT NOT NULL DEFAULT '',
    recommendation_snapshot_hash       TEXT NOT NULL DEFAULT '',
    status                             TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'published', 'discarded')),
    created_at                         TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at                         TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_self_improvement_proposal_bundles_workspace_status
    ON self_improvement_proposal_bundles(workspace_id, status, updated_at DESC);

CREATE TABLE IF NOT EXISTS self_improvement_proposal_bundle_items (
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
    decision              TEXT NOT NULL DEFAULT 'accepted' CHECK (decision IN ('pending', 'accepted', 'rejected', 'linked_existing', 'published', 'discarded')),
    decision_reason       TEXT NOT NULL DEFAULT '',
    published_version_id  TEXT NOT NULL DEFAULT '',
    created_at            TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at            TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_self_improvement_proposal_bundle_items_bundle
    ON self_improvement_proposal_bundle_items(bundle_id, asset_type, id);

CREATE TABLE IF NOT EXISTS self_improvement_proposal_bundle_item_events (
    id          TEXT PRIMARY KEY,
    bundle_id   TEXT NOT NULL REFERENCES self_improvement_proposal_bundles(id) ON DELETE CASCADE,
    item_id     TEXT NOT NULL REFERENCES self_improvement_proposal_bundle_items(id) ON DELETE CASCADE,
    event_type  TEXT NOT NULL,
    actor       TEXT NOT NULL DEFAULT 'system',
    reason      TEXT NOT NULL DEFAULT '',
    before_json TEXT NOT NULL DEFAULT '',
    after_json  TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_self_improvement_proposal_bundle_item_events_item
    ON self_improvement_proposal_bundle_item_events(bundle_id, item_id, created_at);
