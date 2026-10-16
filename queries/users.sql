-- name: CreateUser :one
INSERT INTO users (name, level)
VALUES ($1, $2)
RETURNING *;

-- name: GetUser :one
SELECT * FROM users WHERE id = $1;

-- name: GetUserForUpdate :one
SELECT * FROM users WHERE id = $1 FOR UPDATE;

-- name: ListUsers :many
SELECT * FROM users ORDER BY id;

-- name: CountUsers :one
SELECT count(*) FROM users;

-- name: GrantGold :execrows
UPDATE users
SET gold_available = gold_available + sqlc.arg(amount)::bigint
WHERE id = sqlc.arg(user_id);

-- Conditional update: affects 0 rows when the user cannot afford the amount.
-- name: FreezeGold :execrows
UPDATE users
SET gold_available = gold_available - sqlc.arg(amount)::bigint,
    gold_frozen    = gold_frozen    + sqlc.arg(amount)::bigint
WHERE id = sqlc.arg(user_id)
  AND gold_available >= sqlc.arg(amount)::bigint;

-- name: UnfreezeGold :execrows
UPDATE users
SET gold_available = gold_available + sqlc.arg(amount)::bigint,
    gold_frozen    = gold_frozen    - sqlc.arg(amount)::bigint
WHERE id = sqlc.arg(user_id)
  AND gold_frozen >= sqlc.arg(amount)::bigint;

-- name: SpendFrozenGold :execrows
UPDATE users
SET gold_frozen = gold_frozen - sqlc.arg(amount)::bigint
WHERE id = sqlc.arg(user_id)
  AND gold_frozen >= sqlc.arg(amount)::bigint;
