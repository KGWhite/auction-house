-- Returns 0 rows affected when the (user, entry_type, ref) entry already exists,
-- which callers treat as a duplicate operation.
-- name: InsertLedgerEntry :execrows
INSERT INTO gold_ledger (user_id, entry_type, delta_available, delta_frozen, ref_type, ref_id)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (user_id, entry_type, ref_type, ref_id) DO NOTHING;

-- name: ListLedger :many
SELECT id, user_id, entry_type, delta_available, delta_frozen, ref_type, ref_id, created_at
FROM gold_ledger
WHERE user_id = sqlc.arg(user_id)
  AND (sqlc.arg(before_id)::bigint = 0 OR id < sqlc.arg(before_id)::bigint)
ORDER BY id DESC
LIMIT sqlc.arg(row_limit);
