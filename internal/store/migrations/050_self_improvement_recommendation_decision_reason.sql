-- Store human decision context for terminal recommendation-level rejects.
ALTER TABLE self_improvement_recommendations
ADD COLUMN decision_reason TEXT NOT NULL DEFAULT '';
