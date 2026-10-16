package auction_test

import (
	"context"
	"testing"
	"time"

	"auctionhouse/internal/testutil"
)

// endAuction moves the end time into the past so the auction becomes due.
func endAuction(t *testing.T, e env, id int64) {
	t.Helper()
	testutil.SetEndIn(t, e.pool, id, -time.Second)
}

func TestSettleWithWinner(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	alice := testutil.NewUser(t, e.pool, 15, 5000)
	bob := testutil.NewUser(t, e.pool, 16, 5000)
	a := testutil.NewAuction(t, e.pool, testutil.AuctionOpts{StartPrice: 1000})
	bid(t, e, alice, a, 1000, "a1")
	bid(t, e, bob, a, 1500, "b1")
	endAuction(t, e, a)

	stats, err := e.svc.SettleDue(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Settled != 1 || stats.Expired != 0 || stats.Found != 1 {
		t.Fatalf("stats %+v", stats)
	}

	if got := e.auction(t, a); got.status != "settled" {
		t.Fatalf("status %s", got.status)
	}
	// The winner paid from frozen gold; the loser was refunded when outbid.
	if e.balance(t, bob) != (bal{3500, 0}) || e.balance(t, alice) != (bal{5000, 0}) {
		t.Fatalf("balances bob=%+v alice=%+v", e.balance(t, bob), e.balance(t, alice))
	}
	if n := e.count(t, `SELECT count(*) FROM gold_ledger WHERE user_id=$1 AND entry_type='spend' AND ref_type='auction' AND ref_id=$2 AND delta_available=0 AND delta_frozen=-1500`, bob, a); n != 1 {
		t.Fatalf("spend ledger entries: %d", n)
	}
	if n := e.count(t, `SELECT count(*) FROM items i JOIN auctions a ON a.item_id=i.id WHERE a.id=$1 AND i.owner_id=$2`, a, bob); n != 1 {
		t.Fatal("item not delivered to the winner")
	}
	if n := e.count(t, `SELECT count(*) FROM inbox ib JOIN auctions a ON a.item_id=ib.item_id WHERE a.id=$1 AND ib.user_id=$2 AND ib.auction_id=$1`, a, bob); n != 1 {
		t.Fatalf("inbox rows: %d", n)
	}
	testutil.RequireInvariants(t, e.pool)
}

func TestSettleWithoutBidsExpires(t *testing.T) {
	e := newEnv(t)
	a := testutil.NewAuction(t, e.pool, testutil.AuctionOpts{})
	endAuction(t, e, a)

	stats, err := e.svc.SettleDue(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Expired != 1 || stats.Settled != 0 {
		t.Fatalf("stats %+v", stats)
	}
	if got := e.auction(t, a); got.status != "expired" {
		t.Fatalf("status %s", got.status)
	}
	if n := e.count(t, `SELECT count(*) FROM items i JOIN auctions a ON a.item_id=i.id WHERE a.id=$1 AND i.owner_id IS NULL`, a); n != 1 {
		t.Fatal("an unsold item must stay ownerless")
	}
	if e.count(t, `SELECT count(*) FROM inbox`) != 0 || e.count(t, `SELECT count(*) FROM gold_ledger WHERE entry_type='spend'`) != 0 {
		t.Fatal("expiry must not create inbox rows or spend entries")
	}
	testutil.RequireInvariants(t, e.pool)
}

func TestSettleLeavesRunningAuctionsAlone(t *testing.T) {
	e := newEnv(t)
	alice := testutil.NewUser(t, e.pool, 15, 5000)
	a := testutil.NewAuction(t, e.pool, testutil.AuctionOpts{Duration: time.Hour})
	bid(t, e, alice, a, 1000, "a1")

	stats, err := e.svc.SettleDue(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Found != 0 || e.auction(t, a).status != "active" {
		t.Fatalf("running auction was touched: %+v", stats)
	}
}

func TestSettleIsIdempotent(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	alice := testutil.NewUser(t, e.pool, 15, 5000)
	a := testutil.NewAuction(t, e.pool, testutil.AuctionOpts{})
	bid(t, e, alice, a, 1000, "a1")
	endAuction(t, e, a)

	for i := 0; i < 3; i++ {
		if _, err := e.svc.SettleDue(ctx, 50); err != nil {
			t.Fatal(err)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM gold_ledger WHERE entry_type='spend'`); n != 1 {
		t.Fatalf("%d spend entries after repeated settlement", n)
	}
	if n := e.count(t, `SELECT count(*) FROM inbox`); n != 1 {
		t.Fatalf("%d inbox rows after repeated settlement", n)
	}
	if got := e.balance(t, alice); got != (bal{4000, 0}) {
		t.Fatalf("alice %+v", got)
	}
	testutil.RequireInvariants(t, e.pool)
}

func TestSettleBatchSizeAndOrder(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ids := make([]int64, 5)
	for i := range ids {
		ids[i] = testutil.NewAuction(t, e.pool, testutil.AuctionOpts{})
		// The first created ends first (oldest end_at).
		testutil.SetEndIn(t, e.pool, ids[i], -time.Duration(10-i)*time.Second)
	}
	stats, err := e.svc.SettleDue(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Found != 2 || stats.Expired != 2 {
		t.Fatalf("stats %+v", stats)
	}
	for i, id := range ids {
		want := "active"
		if i < 2 {
			want = "expired"
		}
		if got := e.auction(t, id).status; got != want {
			t.Errorf("auction %d (index %d): status %s, want %s", id, i, got, want)
		}
	}
	// Two more batches finish the rest.
	for i := 0; i < 2; i++ {
		if _, err := e.svc.SettleDue(ctx, 2); err != nil {
			t.Fatal(err)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM auctions WHERE status='active'`); n != 0 {
		t.Fatalf("%d auctions still active", n)
	}
}

func TestSettledAuctionRejectsLateBidAndRefundsNobody(t *testing.T) {
	e := newEnv(t)
	alice := testutil.NewUser(t, e.pool, 15, 5000)
	bob := testutil.NewUser(t, e.pool, 16, 5000)
	a := testutil.NewAuction(t, e.pool, testutil.AuctionOpts{})
	bid(t, e, alice, a, 1000, "a1")
	endAuction(t, e, a)
	if _, err := e.svc.SettleDue(context.Background(), 50); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.PlaceBid(context.Background(), bob, a, 2000, "b1"); err == nil {
		t.Fatal("bid on a settled auction must fail")
	}
	if e.balance(t, bob) != (bal{5000, 0}) || e.balance(t, alice) != (bal{4000, 0}) {
		t.Fatal("balances changed")
	}
	testutil.RequireInvariants(t, e.pool)
}
