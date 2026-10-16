package tests

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"auctionhouse/internal/auction"
	"auctionhouse/internal/testutil"
)

// Three settlement workers race over the same batch of ended auctions. Every
// auction must be settled exactly once: one spend entry, one inbox row.
func TestThreeSettleWorkersNeverSettleTwice(t *testing.T) {
	pool, svc := newService(t)
	ctx := context.Background()
	const withBids, withoutBids = 50, 10

	bidders := make([]int64, 6)
	for i := range bidders {
		bidders[i] = testutil.NewUser(t, pool, 15, 1_000_000)
	}
	var ids []int64
	for i := 0; i < withBids+withoutBids; i++ {
		id := testutil.NewAuction(t, pool, testutil.AuctionOpts{ItemLevel: 15, StartPrice: 1000})
		ids = append(ids, id)
		if i < withBids {
			// One to three bids from different players.
			price := int64(1000)
			for b := 0; b <= i%3; b++ {
				res, err := svc.PlaceBid(ctx, bidders[(i+b)%len(bidders)], id, price, fmt.Sprintf("a%d-b%d", id, b))
				if err != nil {
					t.Fatal(err)
				}
				price = res.MinNextBid
			}
		}
	}
	for _, id := range ids {
		testutil.SetEndIn(t, pool, id, -time.Second)
	}

	const workers = 3
	var wg sync.WaitGroup
	totals := make([]auction.SettleStats, workers)
	start := make(chan struct{})
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for {
				stats, err := svc.SettleDue(ctx, 50)
				if err != nil {
					t.Errorf("worker %d: %v", w, err)
					return
				}
				totals[w].Settled += stats.Settled
				totals[w].Expired += stats.Expired
				totals[w].Skipped += stats.Skipped
				if stats.Found == 0 {
					return
				}
			}
		}()
	}
	close(start)
	wg.Wait()

	var settled, expired int
	for _, s := range totals {
		settled += s.Settled
		expired += s.Expired
	}
	t.Logf("per-worker stats: %+v", totals)
	if settled != withBids || expired != withoutBids {
		t.Fatalf("settled=%d expired=%d, want %d and %d", settled, expired, withBids, withoutBids)
	}

	var spends, inboxRows, active int
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM gold_ledger WHERE entry_type='spend'),
		(SELECT count(*) FROM inbox),
		(SELECT count(*) FROM auctions WHERE status='active')`).Scan(&spends, &inboxRows, &active); err != nil {
		t.Fatal(err)
	}
	if spends != withBids || inboxRows != withBids || active != 0 {
		t.Fatalf("spends=%d inbox=%d active=%d, want %d, %d, 0", spends, inboxRows, active, withBids, withBids)
	}
	testutil.RequireInvariants(t, pool)
}

// Auctions end a few hundred milliseconds from now while players keep bidding
// and workers keep settling. No bid may be accepted after the end time, and
// every settlement must be correct.
func TestBidsAroundTheEndMomentAreCutOffCleanly(t *testing.T) {
	pool, svc := newService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	const auctions, biddersPer = 4, 4

	type result struct {
		mu      sync.Mutex
		maxBid  int64
		maxUser int64
	}
	results := make([]*result, auctions)
	ids := make([]int64, auctions)
	for i := range ids {
		ids[i] = testutil.NewAuction(t, pool, testutil.AuctionOpts{ItemLevel: 15, StartPrice: 1000, Duration: time.Hour})
		results[i] = &result{}
	}
	users := make([][]int64, auctions)
	for i := range users {
		for b := 0; b < biddersPer; b++ {
			users[i] = append(users[i], testutil.NewUser(t, pool, 15, 1_000_000_000_000))
		}
	}
	// Close the auctions 300-600ms from now, on the database clock, then start everything.
	for i, id := range ids {
		testutil.SetEndIn(t, pool, id, time.Duration(300+i*100)*time.Millisecond)
	}

	var bidWG, workerWG sync.WaitGroup
	var rejectedAfterEnd sync.Map
	for i := range ids {
		for b, uid := range users[i] {
			bidWG.Add(1)
			go func() {
				defer bidWG.Done()
				rng := rand.New(rand.NewPCG(uint64(i), uint64(b)))
				next := int64(1000)
				for n := 0; ; n++ {
					res, err := svc.PlaceBid(ctx, uid, ids[i], next, fmt.Sprintf("a%d-u%d-n%d", i, uid, n))
					switch {
					case err == nil:
						next = res.MinNextBid
						r := results[i]
						r.mu.Lock()
						if res.Amount > r.maxBid {
							r.maxBid, r.maxUser = res.Amount, uid
						}
						r.mu.Unlock()
					case errors.Is(err, auction.ErrBidTooLow):
						next, _ = auction.MinNextBidFromError(err)
					case errors.Is(err, auction.ErrAlreadyTopBidder):
					case errors.Is(err, auction.ErrAuctionEnded):
						rejectedAfterEnd.Store(i, true)
						return
					default:
						t.Errorf("auction %d user %d: %v", ids[i], uid, err)
						return
					}
					time.Sleep(time.Duration(rng.IntN(3000)) * time.Microsecond)
				}
			}()
		}
	}

	stop := make(chan struct{})
	for w := 0; w < 3; w++ {
		workerWG.Add(1)
		go func() {
			defer workerWG.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if _, err := svc.SettleDue(ctx, 50); err != nil {
					t.Errorf("settle: %v", err)
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
		}()
	}

	bidWG.Wait() // every bidder has been told its auction ended
	// Let the workers finish settling, then stop them.
	deadline := time.Now().Add(20 * time.Second)
	for {
		var active int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM auctions WHERE status='active'`).Scan(&active); err != nil {
			t.Fatal(err)
		}
		if active == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d auctions never settled", active)
		}
		time.Sleep(20 * time.Millisecond)
	}
	close(stop)
	workerWG.Wait()

	for i, id := range ids {
		if _, ok := rejectedAfterEnd.Load(i); !ok {
			t.Errorf("auction %d: no bidder ever saw ErrAuctionEnded", id)
		}
		var status string
		var price int64
		var top, owner *int64
		if err := pool.QueryRow(ctx, `SELECT a.status, a.current_price, a.top_bidder_id, i.owner_id
			FROM auctions a JOIN items i ON i.id=a.item_id WHERE a.id=$1`, id).Scan(&status, &price, &top, &owner); err != nil {
			t.Fatal(err)
		}
		r := results[i]
		if status != "settled" {
			t.Errorf("auction %d: status %s", id, status)
			continue
		}
		if price != r.maxBid || top == nil || *top != r.maxUser || owner == nil || *owner != r.maxUser {
			t.Errorf("auction %d: price=%d top=%v owner=%v, but the largest accepted bid was %d by %d",
				id, price, ptr(top), ptr(owner), r.maxBid, r.maxUser)
		}
		var spend int64
		if err := pool.QueryRow(ctx, `SELECT COALESCE(SUM(-delta_frozen), 0) FROM gold_ledger WHERE entry_type='spend' AND ref_id=$1`, id).Scan(&spend); err != nil {
			t.Fatal(err)
		}
		if spend != price {
			t.Errorf("auction %d: spent %d, final price %d", id, spend, price)
		}
	}

	// The cut-off: not a single bid is stamped at or after its auction's end time.
	var late int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM bids b JOIN auctions a ON a.id=b.auction_id WHERE b.created_at >= a.end_at`).Scan(&late); err != nil {
		t.Fatal(err)
	}
	if late != 0 {
		t.Fatalf("%d bids were accepted at or after the end time", late)
	}
	testutil.RequireInvariants(t, pool)
}

func ptr(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}
