-- name: ListTodos :many
SELECT id, session_id, order_no, content, status, owner, created_at, updated_at
FROM todos WHERE session_id = ? ORDER BY order_no
;

-- name: DeleteTodosBySession :exec
DELETE FROM todos WHERE session_id = ?
;

-- name: InsertTodo :exec
INSERT INTO todos (id, session_id, order_no, content, status, owner, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
;
