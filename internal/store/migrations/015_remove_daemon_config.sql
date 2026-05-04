-- Phase 15: daemon runtime configuration is process-owned (defaults + env),
-- not mutable fleet state. Keep the generic config table for compatibility
-- with older databases, but remove the historical daemon record so startup
-- cannot inherit stale HTTP/log/processor/proxy settings from SQLite.
DELETE FROM config WHERE key = 'daemon';
