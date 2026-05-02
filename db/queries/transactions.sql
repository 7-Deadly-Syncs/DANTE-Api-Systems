-- name: CreateTransaction :one
INSERT INTO transactions (
    user_id,
    merchant_id,
    account_id,
    amount,
    status,
    idempotency_key,
    legacy_reference_id,
    failure_reason,
    processed_at
) VALUES (
    $1,
    $2,
    $3,
    $4,
    $5,
    $6,
    $7,
    $8,
    $9
)
RETURNING *;

-- name: GetTransactionByID :one
SELECT *
FROM transactions
WHERE id = $1
LIMIT 1;

-- name: GetTransactionByIdempotencyKey :one
SELECT *
FROM transactions
WHERE idempotency_key = $1
LIMIT 1;

-- name: UpdateTransactionStatus :one
UPDATE transactions
SET status = $2,
    failure_reason = $3,
    processed_at = $4,
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: CreateTransactionEvent :one
INSERT INTO transaction_events (
    transaction_id,
    event_type,
    message,
    metadata
) VALUES (
    $1,
    $2,
    $3,
    $4
)
RETURNING *;

-- name: ListTransactionEventsByTransactionID :many
SELECT *
FROM transaction_events
WHERE transaction_id = $1
ORDER BY created_at ASC;
