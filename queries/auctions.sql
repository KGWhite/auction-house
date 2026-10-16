-- The end time is computed from the database clock: now() + the given number of milliseconds.
-- name: CreateAuction :one
INSERT INTO auctions (round_id, item_id, level_bracket, start_price, current_price, end_at)
VALUES (
    sqlc.arg(round_id),
    sqlc.arg(item_id),
    sqlc.arg(level_bracket),
    sqlc.arg(start_price),
    sqlc.arg(start_price),
    now() + sqlc.arg(duration_ms)::bigint * interval '1 millisecond'
)
RETURNING id, round_id, item_id, level_bracket, start_price, current_price, top_bidder_id, status, created_at;

-- Locks the auction row and decides "still open" with the database clock at the
-- moment the lock was granted. clock_timestamp() is used instead of now()
-- because now() is frozen at transaction start and a bid that waited for the
-- lock could otherwise slip in after the end time.
-- name: LockAuctionForBid :one
SELECT a.id,
       a.status,
       a.level_bracket,
       a.start_price,
       a.current_price,
       a.top_bidder_id,
       (clock_timestamp() < a.end_at)::boolean AS is_open,
       clock_timestamp()::timestamptz          AS checked_at
FROM auctions a
WHERE a.id = $1
FOR UPDATE;

-- name: UpdateAuctionTopBid :exec
UPDATE auctions
SET current_price = $2, top_bidder_id = $3
WHERE id = $1;

-- name: GetAuctionView :one
SELECT a.id,
       a.level_bracket,
       a.start_price,
       a.current_price,
       a.top_bidder_id,
       a.status,
       i.id          AS item_id,
       i.level       AS item_level,
       t.name        AS item_name,
       t.type        AS item_type,
       tb.name       AS top_bidder_name,
       (a.top_bidder_id IS NOT NULL AND a.top_bidder_id = sqlc.arg(viewer_id)::bigint)::boolean AS is_mine,
       EXISTS (SELECT 1 FROM bids b WHERE b.auction_id = a.id AND b.user_id = sqlc.arg(viewer_id)::bigint)::boolean AS i_have_bid,
       GREATEST(0, floor(EXTRACT(EPOCH FROM (a.end_at - now())) * 1000))::bigint AS remaining_ms
FROM auctions a
JOIN items i ON i.id = a.item_id
JOIN item_templates t ON t.id = i.template_id
LEFT JOIN users tb ON tb.id = a.top_bidder_id
WHERE a.id = sqlc.arg(auction_id);

-- Filters: bracket < 0 means all brackets, item_type = '' means all types.
-- name: ListAuctionViews :many
SELECT a.id,
       a.level_bracket,
       a.start_price,
       a.current_price,
       a.top_bidder_id,
       a.status,
       i.id          AS item_id,
       i.level       AS item_level,
       t.name        AS item_name,
       t.type        AS item_type,
       tb.name       AS top_bidder_name,
       (a.top_bidder_id IS NOT NULL AND a.top_bidder_id = sqlc.arg(viewer_id)::bigint)::boolean AS is_mine,
       EXISTS (SELECT 1 FROM bids b WHERE b.auction_id = a.id AND b.user_id = sqlc.arg(viewer_id)::bigint)::boolean AS i_have_bid,
       GREATEST(0, floor(EXTRACT(EPOCH FROM (a.end_at - now())) * 1000))::bigint AS remaining_ms
FROM auctions a
JOIN items i ON i.id = a.item_id
JOIN item_templates t ON t.id = i.template_id
LEFT JOIN users tb ON tb.id = a.top_bidder_id
WHERE a.status = 'active'
  AND (sqlc.arg(bracket)::int < 0 OR a.level_bracket = sqlc.arg(bracket)::int)
  AND (sqlc.arg(item_type)::text = '' OR t.type = sqlc.arg(item_type)::text)
ORDER BY
    CASE WHEN sqlc.arg(sort)::text = 'price_asc'  THEN a.current_price END ASC,
    CASE WHEN sqlc.arg(sort)::text = 'price_desc' THEN a.current_price END DESC,
    CASE WHEN sqlc.arg(sort)::text = 'level_desc' THEN i.level END DESC,
    a.end_at ASC,
    a.id ASC
LIMIT 500;

-- Settlement, step 1: find due auctions. Locks taken here are released when the
-- statement's transaction ends; every auction is then settled in its own
-- transaction which locks the row again.
-- name: ListDueAuctionIDs :many
SELECT id
FROM auctions
WHERE status = 'active' AND end_at <= now()
ORDER BY end_at
LIMIT sqlc.arg(batch_size)
FOR UPDATE SKIP LOCKED;

-- Settlement, step 2: lock one due auction. SKIP LOCKED makes concurrent
-- workers (or an in-flight bid) pass instead of waiting; the status and end
-- time are re-checked under the lock.
-- name: LockDueAuctionForSettle :one
SELECT id, item_id, current_price, top_bidder_id
FROM auctions
WHERE id = $1 AND status = 'active' AND end_at <= now()
FOR UPDATE SKIP LOCKED;

-- name: SetAuctionStatus :execrows
UPDATE auctions SET status = $2 WHERE id = $1 AND status = 'active';

-- name: CountAuctionsByStatus :many
SELECT status, count(*) AS n FROM auctions GROUP BY status ORDER BY status;

-- Lock-free read of the pricing fields, used when replaying an idempotent bid.
-- name: GetAuctionPricing :one
SELECT id, start_price, current_price, top_bidder_id FROM auctions WHERE id = $1;
