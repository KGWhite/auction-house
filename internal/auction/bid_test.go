package auction_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"auctionhouse/internal/auction"
	"auctionhouse/internal/config"
	"auctionhouse/internal/testutil"
)

func TestMain(m *testing.M) { os.Exit(testutil.Run(m)) }

type env struct {
	pool *pgxpool.Pool
	svc  *auction.Service
}

func newEnv(t *testing.T) env {
	t.Helper()
	pool := testutil.Pool(t)
	return env{pool: pool, svc: auction.NewService(pool, auction.RulesFromConfig(config.Default()))}
}

type bal struct{ avail, frozen int64 }

func (e env) balance(t *testing.T, uid int64) bal {
	t.Helper()
	var b bal
	if err := e.pool.QueryRow(context.Background(), `SELECT gold_available, gold_frozen FROM users WHERE id=$1`, uid).Scan(&b.avail, &b.frozen); err != nil {
		t.Fatal(err)
	}
	return b
}

type auctionRow struct {
	price  int64
	top    *int64
	status string
}

func (e env) auction(t *testing.T, id int64) auctionRow {
	t.Helper()
	var r auctionRow
	if err := e.pool.QueryRow(context.Background(), `SELECT current_price, top_bidder_id, status FROM auctions WHERE id=$1`, id).Scan(&r.price, &r.top, &r.status); err != nil {
		t.Fatal(err)
	}
	return r
}

func (e env) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (e env) bidCount(t *testing.T, auctionID int64) int {
	return e.count(t, `SELECT count(*) FROM bids WHERE auction_id=$1`, auctionID)
}

func bid(t *testing.T, e env, user, auctionID, amount int64, key string) auction.BidResult {
	t.Helper()
	r, err := e.svc.PlaceBid(context.Background(), user, auctionID, amount, key)
	if err != nil {
		t.Fatalf("PlaceBid(user=%d amount=%d key=%s): %v", user, amount, key, err)
	}
	return r
}

func TestFirstBidAtStartPrice(t *testing.T) {
	e := newEnv(t)
	alice := testutil.NewUser(t, e.pool, 15, 5000)
	a := testutil.NewAuction(t, e.pool, testutil.AuctionOpts{StartPrice: 1000})

	r := bid(t, e, alice, a, 1000, "k1")
	if r.Amount != 1000 || r.CurrentPrice != 1000 || r.MinNextBid != 1050 || r.Replayed || r.AuctionID != a || r.UserID != alice {
		t.Fatalf("unexpected result %+v", r)
	}
	if got := e.balance(t, alice); got != (bal{4000, 1000}) {
		t.Fatalf("balance %+v", got)
	}
	au := e.auction(t, a)
	if au.price != 1000 || au.top == nil || *au.top != alice || au.status != "active" {
		t.Fatalf("auction %+v", au)
	}
	if n := e.count(t, `SELECT count(*) FROM gold_ledger WHERE user_id=$1 AND entry_type='freeze' AND ref_type='bid' AND ref_id=$2 AND delta_available=-1000 AND delta_frozen=1000`, alice, r.BidID); n != 1 {
		t.Fatalf("freeze ledger entry missing (%d)", n)
	}
	testutil.RequireInvariants(t, e.pool)
}

func TestBidBelowStartPriceIsTooLow(t *testing.T) {
	e := newEnv(t)
	alice := testutil.NewUser(t, e.pool, 15, 5000)
	a := testutil.NewAuction(t, e.pool, testutil.AuctionOpts{StartPrice: 1000})

	_, err := e.svc.PlaceBid(context.Background(), alice, a, 999, "k1")
	if !errors.Is(err, auction.ErrBidTooLow) {
		t.Fatalf("got %v", err)
	}
	if min, ok := auction.MinNextBidFromError(err); !ok || min != 1000 {
		t.Fatalf("min next bid = %d (%v), want 1000", min, ok)
	}
	if e.bidCount(t, a) != 0 || e.balance(t, alice) != (bal{5000, 0}) {
		t.Fatal("rejected bid left side effects")
	}
}

func TestOutbidRefundsPreviousTopBidder(t *testing.T) {
	e := newEnv(t)
	alice := testutil.NewUser(t, e.pool, 15, 5000)
	bob := testutil.NewUser(t, e.pool, 16, 5000)
	a := testutil.NewAuction(t, e.pool, testutil.AuctionOpts{StartPrice: 1000})

	bid(t, e, alice, a, 1000, "a1")

	// min next bid = 1000 + max(ceil(50), 10) = 1050
	_, err := e.svc.PlaceBid(context.Background(), bob, a, 1049, "b0")
	if min, ok := auction.MinNextBidFromError(err); !errors.Is(err, auction.ErrBidTooLow) || !ok || min != 1050 {
		t.Fatalf("got %v (min %d)", err, min)
	}
	r := bid(t, e, bob, a, 1050, "b1")
	if r.MinNextBid != 1103 { // 1050 + ceil(52.5)
		t.Fatalf("min next bid = %d, want 1103", r.MinNextBid)
	}

	if got := e.balance(t, alice); got != (bal{5000, 0}) {
		t.Fatalf("alice should be fully refunded, got %+v", got)
	}
	if got := e.balance(t, bob); got != (bal{3950, 1050}) {
		t.Fatalf("bob %+v", got)
	}
	au := e.auction(t, a)
	if au.price != 1050 || au.top == nil || *au.top != bob {
		t.Fatalf("auction %+v", au)
	}
	if n := e.count(t, `SELECT count(*) FROM gold_ledger WHERE user_id=$1 AND entry_type='unfreeze' AND delta_available=1000 AND delta_frozen=-1000`, alice); n != 1 {
		t.Fatalf("unfreeze ledger entry missing (%d)", n)
	}
	testutil.RequireInvariants(t, e.pool)
}

func TestBidMayExceedMinimum(t *testing.T) {
	e := newEnv(t)
	alice := testutil.NewUser(t, e.pool, 15, 5000)
	a := testutil.NewAuction(t, e.pool, testutil.AuctionOpts{StartPrice: 1000})
	bid(t, e, alice, a, 4321, "k1")
	if got := e.auction(t, a).price; got != 4321 {
		t.Fatalf("price %d", got)
	}
	testutil.RequireInvariants(t, e.pool)
}

func TestMinimumIncrementFloorAppliesToCheapAuctions(t *testing.T) {
	e := newEnv(t)
	alice := testutil.NewUser(t, e.pool, 15, 5000)
	bob := testutil.NewUser(t, e.pool, 16, 5000)
	a := testutil.NewAuction(t, e.pool, testutil.AuctionOpts{StartPrice: 100})
	bid(t, e, alice, a, 100, "a1")
	_, err := e.svc.PlaceBid(context.Background(), bob, a, 109, "b1")
	if min, _ := auction.MinNextBidFromError(err); min != 110 { // 5% of 100 is 5, below the floor of 10
		t.Fatalf("min next bid = %d, want 110 (err %v)", min, err)
	}
	bid(t, e, bob, a, 110, "b2")
}

func TestAlreadyTopBidder(t *testing.T) {
	e := newEnv(t)
	alice := testutil.NewUser(t, e.pool, 15, 5000)
	a := testutil.NewAuction(t, e.pool, testutil.AuctionOpts{StartPrice: 1000})
	bid(t, e, alice, a, 1000, "k1")
	_, err := e.svc.PlaceBid(context.Background(), alice, a, 2000, "k2")
	if !errors.Is(err, auction.ErrAlreadyTopBidder) {
		t.Fatalf("got %v", err)
	}
	if e.balance(t, alice) != (bal{4000, 1000}) || e.bidCount(t, a) != 1 {
		t.Fatal("rejected bid left side effects")
	}
}

func TestLevelBracketMismatch(t *testing.T) {
	e := newEnv(t)
	low := testutil.NewUser(t, e.pool, 5, 5000) // bracket 0
	a := testutil.NewAuction(t, e.pool, testutil.AuctionOpts{ItemLevel: 15, StartPrice: 1000})
	_, err := e.svc.PlaceBid(context.Background(), low, a, 1000, "k1")
	if !errors.Is(err, auction.ErrLevelBracketMismatch) {
		t.Fatalf("got %v", err)
	}
	// Bracket boundaries: level 19 and 20 are in different brackets.
	l19 := testutil.NewUser(t, e.pool, 19, 5000)
	l20 := testutil.NewUser(t, e.pool, 20, 5000)
	if _, err := e.svc.PlaceBid(context.Background(), l20, a, 1000, "k2"); !errors.Is(err, auction.ErrLevelBracketMismatch) {
		t.Fatalf("level 20 on bracket 1: %v", err)
	}
	bid(t, e, l19, a, 1000, "k3")
}

func TestInsufficientGoldRollsBackEverything(t *testing.T) {
	e := newEnv(t)
	poor := testutil.NewUser(t, e.pool, 15, 1500)
	rich := testutil.NewUser(t, e.pool, 16, 5000)
	a := testutil.NewAuction(t, e.pool, testutil.AuctionOpts{StartPrice: 1000})
	bid(t, e, rich, a, 1000, "r1")

	_, err := e.svc.PlaceBid(context.Background(), poor, a, 1600, "p1")
	if !errors.Is(err, auction.ErrInsufficientGold) {
		t.Fatalf("got %v", err)
	}
	if e.bidCount(t, a) != 1 {
		t.Fatal("the failed bid row must be rolled back")
	}
	if e.balance(t, poor) != (bal{1500, 0}) || e.balance(t, rich) != (bal{4000, 1000}) {
		t.Fatal("balances changed by a failed bid; the previous top bidder must not be refunded")
	}
	au := e.auction(t, a)
	if au.price != 1000 || *au.top != rich {
		t.Fatalf("auction changed: %+v", au)
	}
	// The same key is free to be used again (the failed attempt left no bid behind).
	bid(t, e, poor, a, 1050, "p1")
	testutil.RequireInvariants(t, e.pool)
}

func TestBidWithAllAvailableGold(t *testing.T) {
	e := newEnv(t)
	u := testutil.NewUser(t, e.pool, 15, 1234)
	a := testutil.NewAuction(t, e.pool, testutil.AuctionOpts{StartPrice: 1000})
	bid(t, e, u, a, 1234, "k1")
	if got := e.balance(t, u); got != (bal{0, 1234}) {
		t.Fatalf("balance %+v", got)
	}
}

func TestEndedAuctionsRejectBids(t *testing.T) {
	e := newEnv(t)
	u := testutil.NewUser(t, e.pool, 15, 5000)
	ended := testutil.NewAuction(t, e.pool, testutil.AuctionOpts{Duration: -time.Second})
	if _, err := e.svc.PlaceBid(context.Background(), u, ended, 1000, "k1"); !errors.Is(err, auction.ErrAuctionEnded) {
		t.Fatalf("past end_at: %v", err)
	}
	for _, status := range []string{"settled", "expired", "cancelled"} {
		a := testutil.NewAuction(t, e.pool, testutil.AuctionOpts{})
		if _, err := e.pool.Exec(context.Background(), `UPDATE auctions SET status=$2 WHERE id=$1`, a, status); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.PlaceBid(context.Background(), u, a, 1000, "k-"+status); !errors.Is(err, auction.ErrAuctionEnded) {
			t.Fatalf("status %s: %v", status, err)
		}
	}
	if e.balance(t, u) != (bal{5000, 0}) {
		t.Fatal("rejected bids must not move gold")
	}
}

func TestAuctionAndUserNotFound(t *testing.T) {
	e := newEnv(t)
	u := testutil.NewUser(t, e.pool, 15, 5000)
	a := testutil.NewAuction(t, e.pool, testutil.AuctionOpts{})
	if _, err := e.svc.PlaceBid(context.Background(), u, 987654, 1000, "k1"); !errors.Is(err, auction.ErrAuctionNotFound) {
		t.Fatalf("auction: %v", err)
	}
	if _, err := e.svc.PlaceBid(context.Background(), 987654, a, 1000, "k2"); !errors.Is(err, auction.ErrUserNotFound) {
		t.Fatalf("user: %v", err)
	}
}

func TestMissingIdempotencyKey(t *testing.T) {
	e := newEnv(t)
	u := testutil.NewUser(t, e.pool, 15, 5000)
	a := testutil.NewAuction(t, e.pool, testutil.AuctionOpts{})
	if _, err := e.svc.PlaceBid(context.Background(), u, a, 1000, ""); !errors.Is(err, auction.ErrMissingIdempotencyKey) {
		t.Fatalf("got %v", err)
	}
}

func TestIdempotentReplay(t *testing.T) {
	e := newEnv(t)
	alice := testutil.NewUser(t, e.pool, 15, 5000)
	bob := testutil.NewUser(t, e.pool, 16, 5000)
	a := testutil.NewAuction(t, e.pool, testutil.AuctionOpts{StartPrice: 1000})

	first := bid(t, e, alice, a, 1000, "same-key")
	again := bid(t, e, alice, a, 1000, "same-key")
	if !again.Replayed || again.BidID != first.BidID || again.Amount != 1000 {
		t.Fatalf("replay %+v vs first %+v", again, first)
	}
	if e.bidCount(t, a) != 1 || e.balance(t, alice) != (bal{4000, 1000}) {
		t.Fatal("replay must not create a second bid or freeze again")
	}

	// A replay of a bid that has since been outbid still returns the original
	// bid (rather than "already top bidder" / "too low") and changes nothing.
	bid(t, e, bob, a, 1050, "bob-1")
	late := bid(t, e, alice, a, 1000, "same-key")
	if !late.Replayed || late.BidID != first.BidID || late.CurrentPrice != 1050 || late.MinNextBid != 1103 {
		t.Fatalf("late replay %+v", late)
	}
	if e.bidCount(t, a) != 2 {
		t.Fatalf("bid count %d", e.bidCount(t, a))
	}

	// Keys are scoped per user.
	other := testutil.NewUser(t, e.pool, 17, 5000)
	if r := bid(t, e, other, a, 2000, "same-key"); r.Replayed {
		t.Fatal("another user's key must not collide")
	}
	testutil.RequireInvariants(t, e.pool)
}

func TestBidTimestampIsBeforeEndTime(t *testing.T) {
	e := newEnv(t)
	u := testutil.NewUser(t, e.pool, 15, 5000)
	a := testutil.NewAuction(t, e.pool, testutil.AuctionOpts{Duration: time.Minute})
	bid(t, e, u, a, 1000, "k1")
	if n := e.count(t, `SELECT count(*) FROM bids b JOIN auctions a ON a.id=b.auction_id WHERE b.created_at >= a.end_at`); n != 0 {
		t.Fatalf("%d bids at or after end_at", n)
	}
}

func TestPingPongBiddingKeepsLedgerBalanced(t *testing.T) {
	e := newEnv(t)
	alice := testutil.NewUser(t, e.pool, 15, 100_000)
	bob := testutil.NewUser(t, e.pool, 16, 100_000)
	a := testutil.NewAuction(t, e.pool, testutil.AuctionOpts{StartPrice: 1000})

	price := int64(1000)
	users := []int64{alice, bob}
	for i := 0; i < 20; i++ {
		r := bid(t, e, users[i%2], a, price, "k"+string(rune('a'+i)))
		price = r.MinNextBid
	}
	testutil.RequireInvariants(t, e.pool)
	// The loser of the last bid has nothing frozen; the winner has exactly the price.
	top := e.auction(t, a)
	for _, u := range users {
		b := e.balance(t, u)
		if u == *top.top && b.frozen != top.price {
			t.Errorf("top bidder frozen %d != price %d", b.frozen, top.price)
		}
		if u != *top.top && b.frozen != 0 {
			t.Errorf("outbid user still has %d frozen", b.frozen)
		}
	}
}
