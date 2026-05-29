-- name: CreateSession :one
INSERT INTO sessions (id, title, cwd, model, status, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING id, title, cwd, model, status, created_at, updated_at
;

-- name: GetSession :one
SELECT id, title, cwd, model, status, created_at, updated_at
FROM sessions WHERE id = ?
;

-- name: ListSessions :many
SELECT id, title, cwd, model, status, created_at, updated_at
FROM sessions
ORDER BY updated_at DESC
LIMIT ? OFFSET ?
;

-- name: UpdateSession :exec
UPDATE sessions SET title = ?, status = ?, updated_at = ? WHERE id = ?
;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE id = ?
;
