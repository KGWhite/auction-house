-- +goose Up

CREATE TABLE users (
    id             bigserial PRIMARY KEY,
    name           text   NOT NULL UNIQUE,
    level          int    NOT NULL CHECK (level >= 0),
    gold_available bigint NOT NULL DEFAULT 0,
    gold_frozen    bigint NOT NULL DEFAULT 0,
    CONSTRAINT users_non_negative_gold CHECK (gold_available >= 0 AND gold_frozen >= 0)
);

CREATE TABLE item_templates (
    id         bigserial PRIMARY KEY,
    name       text   NOT NULL,
    type       text   NOT NULL CHECK (type IN ('weapon', 'shield', 'armor', 'helmet', 'gloves', 'boots', 'ring', 'amulet')),
    base_value bigint NOT NULL CHECK (base_value > 0),
    min_level  int    NOT NULL CHECK (min_level >= 0)
);

CREATE TABLE items (
    id          bigserial PRIMARY KEY,
    template_id bigint NOT NULL REFERENCES item_templates (id),
    level       int    NOT NULL CHECK (level >= 0),
    owner_id    bigint REFERENCES users (id) -- NULL until won at auction
);

CREATE TABLE auction_rounds (
    id         bigserial PRIMARY KEY,
    started_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE auctions (
    id            bigserial PRIMARY KEY,
    round_id      bigint      NOT NULL REFERENCES auction_rounds (id),
    item_id       bigint      NOT NULL UNIQUE REFERENCES items (id),
    level_bracket int         NOT NULL CHECK (level_bracket >= 0),
    start_price   bigint      NOT NULL CHECK (start_price > 0),
    current_price bigint      NOT NULL CHECK (current_price >= start_price), -- equals start_price while there are no bids
    top_bidder_id bigint      REFERENCES users (id),
    end_at        timestamptz NOT NULL, -- never exposed through the API
    status        text        NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'settled', 'expired', 'cancelled')),
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX auctions_status_end_at_idx ON auctions (status, end_at);
CREATE INDEX auctions_bracket_status_idx ON auctions (level_bracket, status);

-- Append-only.
CREATE TABLE bids (
    id              bigserial PRIMARY KEY,
    auction_id      bigint      NOT NULL REFERENCES auctions (id),
    user_id         bigint      NOT NULL REFERENCES users (id),
    amount          bigint      NOT NULL CHECK (amount > 0),
    idempotency_key text        NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, idempotency_key)
);

CREATE INDEX bids_auction_idx ON bids (auction_id, id);

-- Append-only.
CREATE TABLE gold_ledger (
    id              bigserial PRIMARY KEY,
    user_id         bigint      NOT NULL REFERENCES users (id),
    entry_type      text        NOT NULL CHECK (entry_type IN ('grant', 'freeze', 'unfreeze', 'spend')),
    delta_available bigint      NOT NULL,
    delta_frozen    bigint      NOT NULL,
    ref_type        text        NOT NULL,
    ref_id          bigint      NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (user_id, entry_type, ref_type, ref_id)
);

CREATE INDEX gold_ledger_user_idx ON gold_ledger (user_id, id DESC);

CREATE TABLE inbox (
    id         bigserial PRIMARY KEY,
    user_id    bigint      NOT NULL REFERENCES users (id),
    item_id    bigint      REFERENCES items (id),
    auction_id bigint      REFERENCES auctions (id),
    message    text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX inbox_user_idx ON inbox (user_id, id DESC);

-- bids and gold_ledger are append-only: reject UPDATE and DELETE at the database level.
-- +goose StatementBegin
CREATE FUNCTION reject_modification() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION '% is append-only (% rejected)', TG_TABLE_NAME, TG_OP USING ERRCODE = 'restrict_violation';
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER bids_append_only
    BEFORE UPDATE OR DELETE ON bids
    FOR EACH ROW EXECUTE FUNCTION reject_modification();

CREATE TRIGGER gold_ledger_append_only
    BEFORE UPDATE OR DELETE ON gold_ledger
    FOR EACH ROW EXECUTE FUNCTION reject_modification();

-- +goose Down

DROP TRIGGER gold_ledger_append_only ON gold_ledger;
DROP TRIGGER bids_append_only ON bids;
DROP FUNCTION reject_modification();
DROP TABLE inbox;
DROP TABLE gold_ledger;
DROP TABLE bids;
DROP TABLE auctions;
DROP TABLE auction_rounds;
DROP TABLE items;
DROP TABLE item_templates;
DROP TABLE users;
