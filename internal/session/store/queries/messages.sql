-- name: AppendMessage :one
INSERT INTO messages (
    id, session_id, seq_no, role, blocks_json,
    tokens, source_provider, visibility, user_visibility,
    original_ids_json, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id, session_id, seq_no, role, blocks_json,
          tokens, source_provider, visibility, user_visibility,
          original_ids_json, created_at
;

-- name: NextSeqNo :one
SELECT COALESCE(MAX(seq_no), 0) + 1 AS next_seq FROM messages WHERE session_id = ?
;

-- name: ListLiveMessages :many
SELECT id, session_id, seq_no, role, blocks_json,
       tokens, source_provider, visibility, user_visibility,
       original_ids_json, created_at
FROM messages
WHERE session_id = ?
  AND visibility IN ('live', 'summary')
ORDER BY seq_no
;

-- name: ListVisibleMessages :many
SELECT id, session_id, seq_no, role, blocks_json,
       tokens, source_provider, visibility, user_visibility,
       original_ids_json, created_at
FROM messages
WHERE session_id = ?
  AND user_visibility = 'visible'
  AND visibility != 'archived'
ORDER BY seq_no
;

-- name: ListAllMessages :many
SELECT id, session_id, seq_no, role, blocks_json,
       tokens, source_provider, visibility, user_visibility,
       original_ids_json, created_at
FROM messages
WHERE session_id = ?
ORDER BY seq_no
;

-- name: MarkMessageArchived :exec
UPDATE messages
SET visibility = 'archived'
WHERE id = ?
;
