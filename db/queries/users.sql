-- name: CreateUser :one
INSERT INTO users (
    name,
    phone_number
) VALUES (
    $1,
    $2
)
RETURNING *;

-- name: GetUserByID :one
SELECT *
FROM users
WHERE id = $1
LIMIT 1;

-- name: GetUserByPhoneNumber :one
SELECT *
FROM users
WHERE phone_number = $1
LIMIT 1;

-- name: CreateAccount :one
INSERT INTO accounts (
    user_id,
    account_number,
    balance
) VALUES (
    $1,
    $2,
    $3
)
RETURNING *;

-- name: GetAccountByNumber :one
SELECT *
FROM accounts
WHERE account_number = $1
LIMIT 1;

-- name: GetAccountByID :one
SELECT *
FROM accounts
WHERE id = $1
LIMIT 1;

-- name: UpdateAccountBalance :one
UPDATE accounts
SET balance = $2
WHERE id = $1
RETURNING *;
