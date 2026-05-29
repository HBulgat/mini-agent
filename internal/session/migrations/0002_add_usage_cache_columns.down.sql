-- 0002_add_usage_cache_columns.down.sql
-- SQLite supports ALTER TABLE DROP COLUMN as of 3.35 (2021-03). modernc/sqlite
-- bundles a recent build, so the straightforward path works.

ALTER TABLE usage_log DROP COLUMN cache_read_tokens;
ALTER TABLE usage_log DROP COLUMN cache_creation_tokens;
ALTER TABLE usage_log DROP COLUMN cached_prompt_tokens;
