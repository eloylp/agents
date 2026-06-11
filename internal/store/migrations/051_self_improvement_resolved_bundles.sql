-- 051_self_improvement_resolved_bundles: allow final proposal bundles that
-- resolved duplicate/rejected work without publishing a catalog version.

PRAGMA writable_schema = ON;

UPDATE sqlite_schema
SET sql = replace(
    sql,
    'CHECK (status IN (''pending'', ''published'', ''discarded''))',
    'CHECK (status IN (''pending'', ''published'', ''resolved'', ''discarded''))'
)
WHERE type = 'table'
  AND name = 'self_improvement_proposal_bundles'
  AND sql LIKE '%CHECK (status IN (''pending'', ''published'', ''discarded''))%';

PRAGMA writable_schema = OFF;
