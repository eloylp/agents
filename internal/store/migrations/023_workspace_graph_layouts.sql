-- Scope existing agent graph layout rows to the migrated Default workspace.
-- The table already stores a generic scope column from 020_agent_ids_and_graph_layouts.sql.
UPDATE graph_layouts
SET scope = 'workspace:default'
WHERE scope = 'global';
