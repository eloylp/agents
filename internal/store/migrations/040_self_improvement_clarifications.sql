-- 040_self_improvement_clarifications: editable maintainer clarification
-- text for self-improvement recommendations that need more input.

CREATE TABLE IF NOT EXISTS self_improvement_recommendation_clarifications (
    recommendation_id TEXT PRIMARY KEY REFERENCES self_improvement_recommendations(id) ON DELETE CASCADE,
    author            TEXT NOT NULL DEFAULT '',
    body              TEXT NOT NULL DEFAULT '',
    created_at        TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at        TEXT NOT NULL DEFAULT (datetime('now'))
);
