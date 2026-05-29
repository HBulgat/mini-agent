-- 0002_add_usage_cache_columns.up.sql
-- R5 addendum: split out the prompt-cache token accounting that
-- OpenAI / Anthropic / Gemini now report. See:
--   docs/system-design/06-session-storage.md §6.3.4 (R5 revision)
--   docs/system-design/08-llm-providers.md       §8.7

ALTER TABLE usage_log ADD COLUMN cached_prompt_tokens  INTEGER NOT NULL DEFAULT 0;
ALTER TABLE usage_log ADD COLUMN cache_creation_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE usage_log ADD COLUMN cache_read_tokens     INTEGER NOT NULL DEFAULT 0;
