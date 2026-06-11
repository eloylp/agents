-- Recommendation rows no longer have a separate human "accepted" gate before
-- bundle review. Previously accepted rows are now ready proposal records.
UPDATE self_improvement_recommendations
SET status = 'recommended',
    updated_at = datetime('now')
WHERE status = 'accepted';
