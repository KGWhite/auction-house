-- Invariant (a): balances equal ledger sums.
-- name: InvBalanceMismatches :many
SELECT u.id AS user_id,
       u.gold_available,
       u.gold_frozen,
       COALESCE(l.avail, 0)::bigint  AS ledger_available,
       COALESCE(l.frozen, 0)::bigint AS ledger_frozen
FROM users u
LEFT JOIN (
    SELECT user_id, SUM(delta_available) AS avail, SUM(delta_frozen) AS frozen
    FROM gold_ledger
    GROUP BY user_id
) l ON l.user_id = u.id
WHERE u.gold_available <> COALESCE(l.avail, 0)
   OR u.gold_frozen    <> COALESCE(l.frozen, 0)
ORDER BY u.id;

-- Invariant (b): no negative balances.
-- name: InvNegativeBalances :many
SELECT id AS user_id, gold_available, gold_frozen
FROM users
WHERE gold_available < 0 OR gold_frozen < 0
ORDER BY id;

-- Invariant (c): gold conservation.
-- name: InvConservation :one
SELECT
    (SELECT COALESCE(SUM(gold_available + gold_frozen), 0) FROM users)::bigint                      AS total_held,
    (SELECT COALESCE(SUM(-delta_frozen), 0) FROM gold_ledger WHERE entry_type = 'spend')::bigint    AS total_spent,
    (SELECT COALESCE(SUM(delta_available), 0) FROM gold_ledger WHERE entry_type = 'grant')::bigint  AS total_granted;

-- Invariant (d), part 1: on an active auction, a user other than the top bidder
-- must have no outstanding frozen gold for it.
-- name: InvActiveNonTopFrozen :many
WITH net AS (
    SELECT b.auction_id, b.user_id, SUM(l.delta_frozen)::bigint AS net_frozen
    FROM bids b
    JOIN gold_ledger l
      ON l.user_id = b.user_id
     AND l.ref_type = 'bid'
     AND l.ref_id = b.id
     AND l.entry_type IN ('freeze', 'unfreeze')
    GROUP BY b.auction_id, b.user_id
)
SELECT a.id AS auction_id, n.user_id, n.net_frozen
FROM net n
JOIN auctions a ON a.id = n.auction_id
WHERE a.status = 'active'
  AND n.user_id IS DISTINCT FROM a.top_bidder_id
  AND n.net_frozen <> 0
ORDER BY a.id, n.user_id;

-- Invariant (d), part 2: the top bidder's outstanding frozen gold equals current_price.
-- name: InvActiveTopFrozen :many
WITH net AS (
    SELECT b.auction_id, b.user_id, SUM(l.delta_frozen)::bigint AS net_frozen
    FROM bids b
    JOIN gold_ledger l
      ON l.user_id = b.user_id
     AND l.ref_type = 'bid'
     AND l.ref_id = b.id
     AND l.entry_type IN ('freeze', 'unfreeze')
    GROUP BY b.auction_id, b.user_id
)
SELECT a.id AS auction_id, a.top_bidder_id::bigint AS user_id, a.current_price,
       COALESCE(n.net_frozen, 0)::bigint AS net_frozen
FROM auctions a
LEFT JOIN net n ON n.auction_id = a.id AND n.user_id = a.top_bidder_id
WHERE a.status = 'active'
  AND a.top_bidder_id IS NOT NULL
  AND COALESCE(n.net_frozen, 0) <> a.current_price
ORDER BY a.id;

-- Invariant (d), part 3: an active auction without a top bidder is still at its start price.
-- name: InvActiveNoBidderPrice :many
SELECT id AS auction_id, start_price, current_price
FROM auctions
WHERE status = 'active' AND top_bidder_id IS NULL AND current_price <> start_price
ORDER BY id;

-- Invariant (e): a settled auction has the item owned by the top bidder and
-- exactly one spend ledger entry equal to the final price.
-- name: InvSettledViolations :many
SELECT a.id AS auction_id,
       a.top_bidder_id,
       i.owner_id,
       a.current_price,
       COALESCE(s.cnt, 0)::bigint   AS spend_count,
       COALESCE(s.total, 0)::bigint AS spend_total
FROM auctions a
JOIN items i ON i.id = a.item_id
LEFT JOIN (
    SELECT ref_id, count(*) AS cnt, SUM(-delta_frozen) AS total
    FROM gold_ledger
    WHERE entry_type = 'spend' AND ref_type = 'auction'
    GROUP BY ref_id
) s ON s.ref_id = a.id
WHERE a.status = 'settled'
  AND (a.top_bidder_id IS NULL
       OR i.owner_id IS DISTINCT FROM a.top_bidder_id
       OR COALESCE(s.cnt, 0) <> 1
       OR COALESCE(s.total, 0) <> a.current_price)
ORDER BY a.id;

-- Invariant (e), extra: spend entries must belong to settled auctions.
-- name: InvOrphanSpends :many
SELECT l.id AS ledger_id, l.user_id, l.ref_id AS auction_id
FROM gold_ledger l
LEFT JOIN auctions a ON a.id = l.ref_id
WHERE l.entry_type = 'spend'
  AND (l.ref_type <> 'auction' OR a.id IS NULL OR a.status <> 'settled')
ORDER BY l.id;
