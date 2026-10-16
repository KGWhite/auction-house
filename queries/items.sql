-- name: CreateItemTemplate :one
INSERT INTO item_templates (name, type, base_value, min_level)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: CountItemTemplates :one
SELECT count(*) FROM item_templates;

-- Templates whose min_level does not exceed the given level.
-- name: ListTemplatesUpToLevel :many
SELECT * FROM item_templates WHERE min_level <= $1 ORDER BY id;

-- name: CreateItem :one
INSERT INTO items (template_id, level)
VALUES ($1, $2)
RETURNING *;

-- name: SetItemOwner :execrows
UPDATE items SET owner_id = $2 WHERE id = $1;

-- name: GetItem :one
SELECT * FROM items WHERE id = $1;

-- name: CreateAuctionRound :one
INSERT INTO auction_rounds DEFAULT VALUES
RETURNING *;
