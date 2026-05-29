-- 0001_init.up.sql
-- Initial schema for mini-agent. Mirrors docs/system-design/06-session-storage.md §6.3.
-- Cache-token columns are added separately in 0002 to keep this file frozen as
-- the canonical "v1" baseline.

CREATE TABLE sessions (
    id           TEXT    PRIMARY KEY,
    title        TEXT    NOT NULL DEFAULT '',
    cwd          TEXT    NOT NULL DEFAULT '',
    model        TEXT    NOT NULL DEFAULT '',
    status       TEXT    NOT NULL DEFAULT 'active',
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);
CREATE INDEX idx_sessions_updated_at ON sessions(updated_at DESC);

CREATE TABLE messages (
    id                 TEXT    PRIMARY KEY,
    session_id         TEXT    NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    seq_no             INTEGER NOT NULL,
    role               TEXT    NOT NULL,
    blocks_json        TEXT    NOT NULL DEFAULT '[]',
    tokens             INTEGER NOT NULL DEFAULT 0,
    source_provider    TEXT    NOT NULL DEFAULT '',
    visibility         TEXT    NOT NULL DEFAULT 'live',
    user_visibility    TEXT    NOT NULL DEFAULT 'visible',
    original_ids_json  TEXT    NOT NULL DEFAULT '[]',
    created_at         INTEGER NOT NULL
);
CREATE UNIQUE INDEX idx_messages_session_seq        ON messages(session_id, seq_no);
CREATE INDEX        idx_messages_session_visibility ON messages(session_id, visibility, seq_no);
CREATE INDEX        idx_messages_session_user_vis   ON messages(session_id, user_visibility, seq_no);

CREATE TABLE todos (
    id           TEXT    PRIMARY KEY,
    session_id   TEXT    NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    order_no     INTEGER NOT NULL,
    content      TEXT    NOT NULL,
    status       TEXT    NOT NULL DEFAULT 'pending',
    owner        TEXT    NOT NULL DEFAULT 'main',
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);
CREATE INDEX idx_todos_session ON todos(session_id, order_no);

CREATE TABLE usage_log (
    id                TEXT    PRIMARY KEY,
    session_id        TEXT    NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    message_id        TEXT,
    model             TEXT    NOT NULL,
    prompt_tokens     INTEGER NOT NULL DEFAULT 0,
    completion_tokens INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens  INTEGER NOT NULL DEFAULT 0,
    total_tokens      INTEGER NOT NULL DEFAULT 0,
    cost_usd          REAL    NOT NULL DEFAULT 0,
    created_at        INTEGER NOT NULL
);
CREATE INDEX idx_usage_session_time ON usage_log(session_id, created_at);
CREATE INDEX idx_usage_global_time  ON usage_log(created_at);
