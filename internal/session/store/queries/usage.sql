-- name: AddUsage :exec
INSERT INTO usage_log (
    id, session_id, message_id, model,
    prompt_tokens, completion_tokens, reasoning_tokens,
    cached_prompt_tokens, cache_creation_tokens, cache_read_tokens,
    total_tokens, cost_usd, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
;

-- name: SessionUsage :one
SELECT
    COALESCE(SUM(prompt_tokens), 0)         AS prompt_tokens,
    COALESCE(SUM(completion_tokens), 0)     AS completion_tokens,
    COALESCE(SUM(reasoning_tokens), 0)      AS reasoning_tokens,
    COALESCE(SUM(cached_prompt_tokens), 0)  AS cached_prompt_tokens,
    COALESCE(SUM(cache_creation_tokens), 0) AS cache_creation_tokens,
    COALESCE(SUM(cache_read_tokens), 0)     AS cache_read_tokens,
    COALESCE(SUM(total_tokens), 0)          AS total_tokens,
    COALESCE(SUM(cost_usd), 0.0)            AS cost_usd,
    COUNT(*)                                AS requests
FROM usage_log
WHERE session_id = ?
;

-- name: GlobalUsage :one
SELECT
    COALESCE(SUM(prompt_tokens), 0)         AS prompt_tokens,
    COALESCE(SUM(completion_tokens), 0)     AS completion_tokens,
    COALESCE(SUM(reasoning_tokens), 0)      AS reasoning_tokens,
    COALESCE(SUM(cached_prompt_tokens), 0)  AS cached_prompt_tokens,
    COALESCE(SUM(cache_creation_tokens), 0) AS cache_creation_tokens,
    COALESCE(SUM(cache_read_tokens), 0)     AS cache_read_tokens,
    COALESCE(SUM(total_tokens), 0)          AS total_tokens,
    COALESCE(SUM(cost_usd), 0.0)            AS cost_usd,
    COUNT(*)                                AS requests
FROM usage_log
;
