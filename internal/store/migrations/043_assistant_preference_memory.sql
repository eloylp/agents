CREATE TABLE assistant_memory (
	id TEXT PRIMARY KEY,
	key TEXT NOT NULL,
	value TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'active',
	evidence_type TEXT NOT NULL DEFAULT 'manual_user_entry',
	evidence_id TEXT NOT NULL DEFAULT '',
	evidence_url TEXT NOT NULL DEFAULT '',
	confidence TEXT NOT NULL DEFAULT 'medium',
	proposed_by TEXT NOT NULL DEFAULT '',
	rejected_reason TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	updated_at TEXT NOT NULL DEFAULT (datetime('now')),
	approved_at TEXT,
	archived_at TEXT
);

CREATE INDEX idx_assistant_memory_status ON assistant_memory(status, updated_at DESC);
CREATE INDEX idx_assistant_memory_evidence ON assistant_memory(evidence_type, evidence_id);
CREATE UNIQUE INDEX idx_assistant_memory_live_key ON assistant_memory(key COLLATE NOCASE) WHERE status IN ('active', 'proposed');
