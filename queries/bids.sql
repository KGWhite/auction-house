-- created_at is supplied by the caller from the database clock reading taken
-- when the auction row was locked (see LockAuctionForBid).
-- Returns no row when (user_id, idempotency_key) already exists.
-- name: InsertBid :one
INSERT INTO bids (auction_id, user_id, amount, idempotency_key, created_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (user_id, idempotency_key) DO NOTHING
RETURNING *;

-- name: GetBidByIdempotencyKey :one
SELECT * FROM bids WHERE user_id = $1 AND idempotency_key = $2;

-- The latest bid of an auction is always the top bid.
-- name: GetLatestBid :one
SELECT * FROM bids WHERE auction_id = $1 ORDER BY id DESC LIMIT 1;

-- name: CountBidsForAuction :one
SELECT count(*) FROM bids WHERE auction_id = $1;
