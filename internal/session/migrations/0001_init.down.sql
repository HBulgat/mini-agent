-- 0001_init.down.sql
-- Reverse the v1 baseline. Drops in dependency order so foreign keys
-- never refer to a missing parent.

DROP INDEX IF EXISTS idx_usage_global_time;
DROP INDEX IF EXISTS idx_usage_session_time;
DROP TABLE IF EXISTS usage_log;

DROP INDEX IF EXISTS idx_todos_session;
DROP TABLE IF EXISTS todos;

DROP INDEX IF EXISTS idx_messages_session_user_vis;
DROP INDEX IF EXISTS idx_messages_session_visibility;
DROP INDEX IF EXISTS idx_messages_session_seq;
DROP TABLE IF EXISTS messages;

DROP INDEX IF EXISTS idx_sessions_updated_at;
DROP TABLE IF EXISTS sessions;
