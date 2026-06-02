-- 041_self_improvement_proposal_bundles: editable, inert staging records
-- for reactive multi-asset self-improvement catalog changes.

CREATE TABLE IF NOT EXISTS self_improvement_proposal_bundles (
    id                TEXT PRIMARY KEY,
    workspace_id      TEXT NOT NULL DEFAULT 'default',
    recommendation_id TEXT NOT NULL UNIQUE REFERENCES self_improvement_recommendations(id) ON DELETE CASCADE,
    status            TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'published', 'partially_published', 'discarded', 'stale')),
    created_at        TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at        TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_self_improvement_proposal_bundles_workspace_status
    ON self_improvement_proposal_bundles(workspace_id, status, updated_at DESC);

CREATE TABLE IF NOT EXISTS self_improvement_proposal_bundle_items (
    id                    TEXT PRIMARY KEY,
    bundle_id             TEXT NOT NULL REFERENCES self_improvement_proposal_bundles(id) ON DELETE CASCADE,
    operation             TEXT NOT NULL CHECK (operation IN ('update_existing', 'create_new', 'link_existing')),
    asset_type            TEXT NOT NULL CHECK (asset_type IN ('prompt', 'skill', 'guardrail')),
    asset_id              TEXT NOT NULL DEFAULT '',
    base_version_id       TEXT NOT NULL DEFAULT '',
    proposed_ref          TEXT NOT NULL DEFAULT '',
    proposed_name         TEXT NOT NULL DEFAULT '',
    proposed_scope        TEXT NOT NULL DEFAULT '',
    proposed_body         TEXT NOT NULL DEFAULT '',
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
