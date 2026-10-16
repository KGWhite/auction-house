package db_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"auctionhouse/internal/testutil"
)

func TestMain(m *testing.M) { os.Exit(testutil.Run(m)) }

func TestNegativeGoldRejected(t *testing.T) {
	pool := testutil.Pool(t)
	uid := testutil.NewUser(t, pool, 10, 100)
	_, err := pool.Exec(context.Background(), `UPDATE users SET gold_available = -1 WHERE id = $1`, uid)
	if err == nil {
		t.Fatal("expected CHECK violation for negative gold_available")
	}
	_, err = pool.Exec(context.Background(), `UPDATE users SET gold_frozen = -1 WHERE id = $1`, uid)
	if err == nil {
		t.Fatal("expected CHECK violation for negative gold_frozen")
	}
}

func TestLedgerAndBidsAreAppendOnly(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	uid := testutil.NewUser(t, pool, 10, 100)
	aid := testutil.NewAuction(t, pool, testutil.AuctionOpts{})
	if _, err := pool.Exec(ctx, `INSERT INTO bids (auction_id, user_id, amount, idempotency_key) VALUES ($1, $2, 1000, 'k')`, aid, uid); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`UPDATE gold_ledger SET delta_available = 0`,
		`DELETE FROM gold_ledger`,
		`UPDATE bids SET amount = 1`,
		`DELETE FROM bids`,
	} {
		_, err := pool.Exec(ctx, stmt)
		if err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Errorf("%q: expected append-only rejection, got %v", stmt, err)
		}
	}
}

func TestUniqueConstraints(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	uid := testutil.NewUser(t, pool, 10, 100)
	aid := testutil.NewAuction(t, pool, testutil.AuctionOpts{})

	ins := `INSERT INTO bids (auction_id, user_id, amount, idempotency_key) VALUES ($1, $2, 1000, 'same')`
	if _, err := pool.Exec(ctx, ins, aid, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, ins, aid, uid); err == nil {
		t.Error("duplicate (user_id, idempotency_key) must be rejected")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO gold_ledger (user_id, entry_type, delta_available, delta_frozen, ref_type, ref_id)
		VALUES ($1, 'grant', 1, 0, 'seed', $1)`, uid); err == nil {
		t.Error("duplicate ledger (user, entry_type, ref) must be rejected")
	}
	if _, err := pool.Exec(ctx, `INSERT INTO auctions (round_id, item_id, level_bracket, start_price, current_price, end_at)
		SELECT round_id, item_id, level_bracket, start_price, current_price, end_at FROM auctions WHERE id = $1`, aid); err == nil {
		t.Error("an item can be in only one auction")
	}
}

func TestEnumChecks(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO item_templates (name, type, base_value, min_level) VALUES ('x', 'sword', 1, 1)`); err == nil {
		t.Error("unknown item type must be rejected")
	}
	aid := testutil.NewAuction(t, pool, testutil.AuctionOpts{})
	if _, err := pool.Exec(ctx, `UPDATE auctions SET status = 'weird' WHERE id = $1`, aid); err == nil {
		t.Error("unknown auction status must be rejected")
	}
	uid := testutil.NewUser(t, pool, 1, 0)
	if _, err := pool.Exec(ctx, `INSERT INTO gold_ledger (user_id, entry_type, delta_available, delta_frozen, ref_type, ref_id)
		VALUES ($1, 'bonus', 1, 0, 'x', 1)`, uid); err == nil {
		t.Error("unknown ledger entry type must be rejected")
	}
}
