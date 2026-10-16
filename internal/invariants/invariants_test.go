package invariants_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"auctionhouse/internal/db"
	"auctionhouse/internal/invariants"
	"auctionhouse/internal/testutil"
	"auctionhouse/internal/wallet"
)

func TestMain(m *testing.M) { os.Exit(testutil.Run(m)) }

func codes(t *testing.T, pool *pgxpool.Pool) map[string]int {
	t.Helper()
	vs, err := invariants.CheckInvariants(context.Background(), pool)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for _, v := range vs {
		out[v.Code]++
	}
	return out
}

func requireOnly(t *testing.T, got map[string]int, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("violations %v, want exactly codes %v", got, want)
	}
	for _, c := range want {
		if got[c] == 0 {
			t.Fatalf("violations %v, missing %s", got, c)
		}
	}
}

// placeBidDirect records a bid the way PlaceBid would (insert, freeze, unfreeze
// the previous top bidder, update the auction) without any validation.
func placeBidDirect(t *testing.T, pool *pgxpool.Pool, auctionID, userID, amount int64, key string) {
	t.Helper()
	if err := tryBidDirect(pool, auctionID, userID, amount, key); err != nil {
		t.Fatal(err)
	}
}

func tryBidDirect(pool *pgxpool.Pool, auctionID, userID, amount int64, key string) error {
	ctx := context.Background()
	return db.InTx(ctx, pool, func(q *db.Queries) error {
		a, err := q.LockAuctionForBid(ctx, auctionID)
		if err != nil {
			return err
		}
		var prev *db.Bid
		if a.TopBidderID != nil {
			b, err := q.GetLatestBid(ctx, auctionID)
			if err != nil {
				return err
			}
			prev = &b
		}
		bid, err := q.InsertBid(ctx, db.InsertBidParams{AuctionID: auctionID, UserID: userID, Amount: amount, IdempotencyKey: key, CreatedAt: a.CheckedAt})
		if err != nil {
			return err
		}
		if err := wallet.Freeze(ctx, q, userID, amount, bid.ID); err != nil {
			return err
		}
		if prev != nil {
			if err := wallet.Unfreeze(ctx, q, prev.UserID, prev.Amount, prev.ID); err != nil {
				return err
			}
		}
		return q.UpdateAuctionTopBid(ctx, db.UpdateAuctionTopBidParams{ID: auctionID, CurrentPrice: amount, TopBidderID: &userID})
	})
}

func TestEmptyDatabaseHolds(t *testing.T) {
	pool := testutil.Pool(t)
	requireOnly(t, codes(t, pool))
}

func TestHealthyStateHolds(t *testing.T) {
	pool := testutil.Pool(t)
	a := testutil.NewUser(t, pool, 15, 5000)
	b := testutil.NewUser(t, pool, 16, 5000)
	auction := testutil.NewAuction(t, pool, testutil.AuctionOpts{})
	placeBidDirect(t, pool, auction, a, 1000, "k1")
	placeBidDirect(t, pool, auction, b, 1050, "k2")
	placeBidDirect(t, pool, auction, a, 1103, "k3")
	requireOnly(t, codes(t, pool))
}

func TestDetectsBalanceLedgerMismatchAndBrokenConservation(t *testing.T) {
	pool := testutil.Pool(t)
	uid := testutil.NewUser(t, pool, 15, 1000)
	if _, err := pool.Exec(context.Background(), `UPDATE users SET gold_available = gold_available + 1 WHERE id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	requireOnly(t, codes(t, pool), invariants.CodeBalanceMismatch, invariants.CodeConservation)
}

func TestDetectsNegativeBalance(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	uid := testutil.NewUser(t, pool, 15, 1000)
	if _, err := pool.Exec(ctx, `ALTER TABLE users DROP CONSTRAINT users_non_negative_gold`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Restore the constraint for later tests (data is truncated by the next Pool call).
		_, _ = pool.Exec(ctx, `TRUNCATE users CASCADE`)
		_, _ = pool.Exec(ctx, `ALTER TABLE users ADD CONSTRAINT users_non_negative_gold CHECK (gold_available >= 0 AND gold_frozen >= 0)`)
	})
	if _, err := pool.Exec(ctx, `UPDATE users SET gold_available = -5 WHERE id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	got := codes(t, pool)
	if got[invariants.CodeNegativeBalance] != 1 {
		t.Fatalf("expected negative balance violation, got %v", got)
	}
}

func TestDetectsActiveAuctionFreezeProblems(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	a := testutil.NewUser(t, pool, 15, 5000)
	b := testutil.NewUser(t, pool, 16, 5000)
	auction := testutil.NewAuction(t, pool, testutil.AuctionOpts{})
	placeBidDirect(t, pool, auction, a, 1000, "k1")
	requireOnly(t, codes(t, pool))

	t.Run("top bidder frozen amount differs from current_price", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `UPDATE auctions SET current_price = 1500 WHERE id = $1`, auction); err != nil {
			t.Fatal(err)
		}
		requireOnly(t, codes(t, pool), invariants.CodeActiveTopFrozen)
		if _, err := pool.Exec(ctx, `UPDATE auctions SET current_price = 1000 WHERE id = $1`, auction); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("outbid user keeps frozen gold", func(t *testing.T) {
		// b bids and gets frozen, but the previous top bidder (a) is never refunded.
		err := db.InTx(ctx, pool, func(q *db.Queries) error {
			l, err := q.LockAuctionForBid(ctx, auction)
			if err != nil {
				return err
			}
			bid, err := q.InsertBid(ctx, db.InsertBidParams{AuctionID: auction, UserID: b, Amount: 1050, IdempotencyKey: "k2", CreatedAt: l.CheckedAt})
			if err != nil {
				return err
			}
			if err := wallet.Freeze(ctx, q, b, 1050, bid.ID); err != nil {
				return err
			}
			return q.UpdateAuctionTopBid(ctx, db.UpdateAuctionTopBidParams{ID: auction, CurrentPrice: 1050, TopBidderID: &b})
		})
		if err != nil {
			t.Fatal(err)
		}
		requireOnly(t, codes(t, pool), invariants.CodeActiveNonTop)
	})
}

func TestDetectsActiveAuctionWithoutBidderAtWrongPrice(t *testing.T) {
	pool := testutil.Pool(t)
	auction := testutil.NewAuction(t, pool, testutil.AuctionOpts{StartPrice: 1000})
	if _, err := pool.Exec(context.Background(), `UPDATE auctions SET current_price = 1200 WHERE id = $1`, auction); err != nil {
		t.Fatal(err)
	}
	requireOnly(t, codes(t, pool), invariants.CodeActiveNoBidder)
}

func TestDetectsBrokenSettledAuction(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	a := testutil.NewUser(t, pool, 15, 5000)
	auction := testutil.NewAuction(t, pool, testutil.AuctionOpts{Duration: -time.Minute})
	placeBidDirect(t, pool, auction, a, 1000, "k1")

	// Marked settled but no item owner and no spend entry.
	if _, err := pool.Exec(ctx, `UPDATE auctions SET status = 'settled' WHERE id = $1`, auction); err != nil {
		t.Fatal(err)
	}
	if got := codes(t, pool); got[invariants.CodeSettledAuction] != 1 {
		t.Fatalf("got %v", got)
	}

	// Add the owner and the spend (balances consistent): everything holds.
	err := db.InTx(ctx, pool, func(q *db.Queries) error {
		var itemID int64
		if err := pool.QueryRow(ctx, `SELECT item_id FROM auctions WHERE id = $1`, auction).Scan(&itemID); err != nil {
			return err
		}
		if _, err := q.SetItemOwner(ctx, db.SetItemOwnerParams{ID: itemID, OwnerID: &a}); err != nil {
			return err
		}
		return wallet.Spend(ctx, q, a, 1000, auction)
	})
	if err != nil {
		t.Fatal(err)
	}
	requireOnly(t, codes(t, pool))

	// A second spend for the same auction cannot be written (unique ledger key),
	// but an amount different from the price is detected.
	if _, err := pool.Exec(ctx, `UPDATE auctions SET current_price = 1100 WHERE id = $1`, auction); err != nil {
		t.Fatal(err)
	}
	if got := codes(t, pool); got[invariants.CodeSettledAuction] != 1 {
		t.Fatalf("price mismatch not detected: %v", got)
	}
}

func TestDetectsOrphanSpend(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	a := testutil.NewUser(t, pool, 15, 5000)
	auction := testutil.NewAuction(t, pool, testutil.AuctionOpts{})
	placeBidDirect(t, pool, auction, a, 1000, "k1")
	// Spend although the auction is still active.
	if err := db.InTx(ctx, pool, func(q *db.Queries) error { return wallet.Spend(ctx, q, a, 1000, auction) }); err != nil {
		t.Fatal(err)
	}
	got := codes(t, pool)
	if got[invariants.CodeOrphanSpend] != 1 {
		t.Fatalf("got %v", got)
	}
}

// CheckInvariants reads one REPEATABLE READ snapshot, so it must never report
// a violation while correct bids are being committed concurrently.
func TestSnapshotIsConsistentUnderConcurrentWrites(t *testing.T) {
	pool := testutil.Pool(t)
	a := testutil.NewUser(t, pool, 15, 1_000_000)
	b := testutil.NewUser(t, pool, 16, 1_000_000)
	auction := testutil.NewAuction(t, pool, testutil.AuctionOpts{})

	done := make(chan struct{})
	go func() {
		defer close(done)
		amount := int64(1000)
		for i := 0; i < 150; i++ {
			user := a
			if i%2 == 1 {
				user = b
			}
			if err := tryBidDirect(pool, auction, user, amount, fmt.Sprintf("k%d", i)); err != nil {
				t.Errorf("bid %d: %v", i, err)
				return
			}
			amount += 60
		}
	}()
	for {
		select {
		case <-done:
			requireOnly(t, codes(t, pool))
			return
		default:
			requireOnly(t, codes(t, pool))
		}
	}
}
