-- name: CreateLegacyCallLog :one
INSERT INTO legacy_call_logs (
    transaction_id,
    endpoint,
    method,
    status_code,
    success,
    latency_ms,
    error_message
) VALUES (
    $1,
    $2,
    $3,
    $4,
    $5,
    $6,
    $7
)
RETURNING *;

-- name: ListLegacyCallLogsByTransactionID :many
SELECT *
FROM legacy_call_logs
WHERE transaction_id = $1
ORDER BY created_at ASC;

-- name: GetLegacyCallMetrics :many
SELECT
    endpoint,
    success,
    COUNT(*)::bigint AS calls_total,
    COALESCE(AVG(latency_ms), 0)::bigint AS avg_latency_ms
FROM legacy_call_logs
GROUP BY endpoint, success
ORDER BY endpoint, success;
