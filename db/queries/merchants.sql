-- name: CreateMerchant :one
INSERT INTO merchants (
    name,
    qris_code,
    category
) VALUES (
    $1,
    $2,
    $3
)
RETURNING *;

-- name: GetMerchantByQRISCode :one
SELECT *
FROM merchants
WHERE qris_code = $1
LIMIT 1;
