-- name: InsertInbox :one
INSERT INTO inbox (user_id, item_id, auction_id, message)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: CountInbox :one
SELECT count(*) FROM inbox WHERE user_id = $1;

-- name: ListInbox :many
SELECT ib.id,
       ib.message,
       ib.created_at,
       ib.auction_id,
       i.id    AS item_id,
       i.level AS item_level,
       t.name  AS item_name,
       t.type  AS item_type,
       a.current_price AS price_paid
FROM inbox ib
LEFT JOIN items i ON i.id = ib.item_id
LEFT JOIN item_templates t ON t.id = i.template_id
LEFT JOIN auctions a ON a.id = ib.auction_id
WHERE ib.user_id = $1
ORDER BY ib.id DESC;
