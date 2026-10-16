package tests

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"auctionhouse/internal/auction"
	"auctionhouse/internal/config"
	"auctionhouse/internal/testutil"
)

func TestMain(m *testing.M) { os.Exit(testutil.Run(m)) }

func newService(t *testing.T) (*pgxpool.Pool, *auction.Service) {
	t.Helper()
	pool := testutil.Pool(t)
	return pool, auction.NewService(pool, auction.RulesFromConfig(config.Default()))
}

func newRand(t *testing.T) *rand.Rand {
	seed := uint64(time.Now().UnixNano())
	t.Logf("random seed: %d", seed)
	return rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
}

// A rejection is expected under contention when it is one of the business
// rules; anything else (deadlock, serialization failure, ...) is a bug.
func expectedBidRejection(err error) bool {
	return errors.Is(err, auction.ErrBidTooLow) || errors.Is(err, auction.ErrAlreadyTopBidder)
}

// 100 goroutines bid random amounts on one auction at once. The final price
// must equal the largest accepted bid, held by that bid's owner.
func TestHundredConcurrentBiddersOnOneAuction(t *testing.T) {
	pool, svc := newService(t)
	ctx := context.Background()
	rng := newRand(t)
	const bidders, roundsEach = 100, 3

	a := testutil.NewAuction(t, pool, testutil.AuctionOpts{ItemLevel: 15, StartPrice: 1000})
	users := make([]int64, bidders)
	amounts := make([][]int64, bidders)
	for i := range users {
		users[i] = testutil.NewUser(t, pool, 10+i%10, 1_000_000)
		for r := 0; r < roundsEach; r++ {
			amounts[i] = append(amounts[i], 1000+rng.Int64N(100_000))
		}
	}

	type accepted struct{ user, amount int64 }
	var mu sync.Mutex
	var wins []accepted
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range users {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for r, amount := range amounts[i] {
				res, err := svc.PlaceBid(ctx, users[i], a, amount, fmt.Sprintf("u%d-r%d", i, r))
				switch {
				case err == nil:
					mu.Lock()
					wins = append(wins, accepted{users[i], res.Amount})
					mu.Unlock()
				case expectedBidRejection(err):
				default:
					t.Errorf("user %d: unexpected error: %v", users[i], err)
				}
			}
		}()
	}
	close(start)
	wg.Wait()

	if len(wins) == 0 {
		t.Fatal("no bid was accepted")
	}
	best := wins[0]
	for _, w := range wins {
		if w.amount > best.amount {
			best = w
		}
	}
	var price, top int64
	if err := pool.QueryRow(ctx, `SELECT current_price, top_bidder_id FROM auctions WHERE id=$1`, a).Scan(&price, &top); err != nil {
		t.Fatal(err)
	}
	if price != best.amount || top != best.user {
		t.Fatalf("auction ended at price=%d top=%d, but the largest accepted bid was %d by %d (%d accepted)",
			price, top, best.amount, best.user, len(wins))
	}
	var bids int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM bids WHERE auction_id=$1`, a).Scan(&bids); err != nil {
		t.Fatal(err)
	}
	if bids != len(wins) {
		t.Fatalf("%d bid rows but %d accepted results", bids, len(wins))
	}
	// Accepted bids are strictly increasing in insertion order (every bid had to beat the one before).
	rows, err := pool.Query(ctx, `SELECT amount FROM bids WHERE auction_id=$1 ORDER BY id`, a)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var last int64
	for rows.Next() {
		var amt int64
		if err := rows.Scan(&amt); err != nil {
			t.Fatal(err)
		}
		if amt <= last {
			t.Fatalf("bid %d does not exceed the previous accepted bid %d", amt, last)
		}
		last = amt
	}
	testutil.RequireInvariants(t, pool)
}

// Two players outbid each other across many auctions at the same time. Every
// bid locks the same two user rows; without the ascending lock order this
// deadlocks within moments.
func TestTwoPlayersOutbidEachOtherAcrossManyAuctionsWithoutDeadlock(t *testing.T) {
	pool, svc := newService(t)
	ctx := context.Background()
	const auctions, rounds = 12, 40

	alice := testutil.NewUser(t, pool, 15, 50_000_000)
	bob := testutil.NewUser(t, pool, 16, 50_000_000)
	ids := make([]int64, auctions)
	for i := range ids {
		ids[i] = testutil.NewAuction(t, pool, testutil.AuctionOpts{ItemLevel: 15, StartPrice: 1000})
	}

	// Seed state: alice leads the even auctions, bob the odd ones, so that the
	// concurrent counter-bids below involve (alice, bob) as (new, previous) on
	// half of the auctions and as (previous, new) on the other half.
	for i, id := range ids {
		leader := alice
		if i%2 == 1 {
			leader = bob
		}
		if _, err := svc.PlaceBid(ctx, leader, id, 1000, fmt.Sprintf("seed-%d", i)); err != nil {
			t.Fatal(err)
		}
	}

	var wg sync.WaitGroup
	var accepted atomic.Int64
	start := make(chan struct{})
	for i, id := range ids {
		for _, u := range []int64{alice, bob} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				next := int64(1000)
				for r := 0; r < rounds; r++ {
					res, err := svc.PlaceBid(ctx, u, id, next, fmt.Sprintf("a%d-u%d-r%d", i, u, r))
					switch {
					case err == nil:
						accepted.Add(1)
						next = res.MinNextBid
					case errors.Is(err, auction.ErrBidTooLow):
						next, _ = auction.MinNextBidFromError(err)
					case errors.Is(err, auction.ErrAlreadyTopBidder):
						time.Sleep(time.Millisecond) // let the other player move
					default:
						t.Errorf("auction %d user %d round %d: %v", id, u, r, err)
						return
					}
				}
			}()
		}
	}
	close(start)
	wg.Wait()

	if accepted.Load() < auctions {
		t.Fatalf("suspiciously few accepted bids: %d", accepted.Load())
	}
	t.Logf("%d bids accepted across %d auctions", accepted.Load(), auctions)
	testutil.RequireInvariants(t, pool)
}

// The same Idempotency-Key delivered 20 times at once creates exactly one bid;
// every delivery gets a success answer for that same bid.
func TestSameIdempotencyKeySentTwentyTimesConcurrently(t *testing.T) {
	pool, svc := newService(t)
	ctx := context.Background()
	user := testutil.NewUser(t, pool, 15, 10_000)
	a := testutil.NewAuction(t, pool, testutil.AuctionOpts{ItemLevel: 15, StartPrice: 1000})

	const sends = 20
	results := make([]auction.BidResult, sends)
	errs := make([]error, sends)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < sends; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results[i], errs[i] = svc.PlaceBid(ctx, user, a, 1500, "one-key-for-all")
		}()
	}
	close(start)
	wg.Wait()

	fresh := 0
	var bidID int64
	for i := range results {
		if errs[i] != nil {
			t.Fatalf("delivery %d failed: %v", i, errs[i])
		}
		if !results[i].Replayed {
			fresh++
		}
		if bidID == 0 {
			bidID = results[i].BidID
		}
		if results[i].BidID != bidID || results[i].Amount != 1500 {
			t.Fatalf("delivery %d returned %+v, want bid %d", i, results[i], bidID)
		}
	}
	if fresh != 1 {
		t.Fatalf("%d deliveries were executed, want exactly 1", fresh)
	}
	var bids int
	var avail, frozen int64
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM bids), gold_available, gold_frozen FROM users WHERE id=$1`, user).Scan(&bids, &avail, &frozen); err != nil {
		t.Fatal(err)
	}
	if bids != 1 || avail != 8500 || frozen != 1500 {
		t.Fatalf("bids=%d available=%d frozen=%d, want 1, 8500, 1500", bids, avail, frozen)
	}
	testutil.RequireInvariants(t, pool)
}

// Many users retry the same key concurrently while others bid too; replays
// never double-charge anyone.
func TestConcurrentRetriesAmongOtherBidders(t *testing.T) {
	pool, svc := newService(t)
	ctx := context.Background()
	a := testutil.NewAuction(t, pool, testutil.AuctionOpts{ItemLevel: 15, StartPrice: 1000})
	const users, retries = 10, 5
	ids := make([]int64, users)
	for i := range ids {
		ids[i] = testutil.NewUser(t, pool, 15, 1_000_000)
	}
	var wg sync.WaitGroup
	for i, uid := range ids {
		for r := 0; r < retries; r++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := svc.PlaceBid(ctx, uid, a, 1000+int64(i)*100, fmt.Sprintf("key-%d", i))
				if err != nil && !expectedBidRejection(err) {
					t.Errorf("user %d: %v", uid, err)
				}
			}()
		}
	}
	wg.Wait()
	var maxPerUser int
	if err := pool.QueryRow(ctx, `SELECT COALESCE(MAX(c), 0) FROM (SELECT count(*) AS c FROM bids GROUP BY user_id) s`).Scan(&maxPerUser); err != nil {
		t.Fatal(err)
	}
	if maxPerUser > 1 {
		t.Fatalf("a user has %d bids from a single idempotency key", maxPerUser)
	}
	testutil.RequireInvariants(t, pool)
}
