CREATE TABLE IF NOT EXISTS catalog_delegation (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	enabled INTEGER NOT NULL DEFAULT 0,
	repo TEXT NOT NULL DEFAULT '',
	branch TEXT NOT NULL DEFAULT 'main',
	catalog_path TEXT NOT NULL DEFAULT 'catalog.yml',
	last_synced_commit TEXT NOT NULL DEFAULT '',
	last_successful_sync_at TEXT NOT NULL DEFAULT '',
	last_sync_status TEXT NOT NULL DEFAULT 'disabled',
	last_sync_error TEXT NOT NULL DEFAULT '',
	disabled_at TEXT NOT NULL DEFAULT '',
	credential_secret TEXT NOT NULL DEFAULT '',
	credential_status TEXT NOT NULL DEFAULT 'unset',
	created_at TEXT NOT NULL DEFAULT (datetime('now')),
	updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

INSERT OR IGNORE INTO catalog_delegation (id) VALUES (1);

