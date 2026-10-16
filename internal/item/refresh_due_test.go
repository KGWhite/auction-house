package item_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"auctionhouse/internal/config"
	"auctionhouse/internal/db"
	"auctionhouse/internal/item"
	"auctionhouse/internal/testutil"
)

func TestRefreshIfDueWaitsForTemplates(t *testing.T) {
	pool := testutil.Pool(t)
	cfg := config.Default()
	if _, created, err := item.RefreshIfDue(context.Background(), pool, cfg, nil); err != nil || created {
		t.Fatalf("no templates: created=%v err=%v", created, err)
	}
	var rounds int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM auction_rounds`).Scan(&rounds); err != nil || rounds != 0 {
		t.Fatalf("rounds=%d err=%v, want 0", rounds, err)
	}
}

func TestRefreshIfDueCreatesRoundOncePerInterval(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	if _, err := item.SeedTemplates(ctx, db.New(pool)); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.RefreshInterval = 3_600_000_000_000 // 1h

	res, created, err := item.RefreshIfDue(ctx, pool, cfg, nil)
	if err != nil || !created || res.Auctions != 48 {
		t.Fatalf("first call: %+v created=%v err=%v", res, created, err)
	}
	if _, created, err := item.RefreshIfDue(ctx, pool, cfg, nil); err != nil || created {
		t.Fatalf("second call right after must not create a round (created=%v err=%v)", created, err)
	}

	// Age the round past the interval (database clock) and it becomes due again.
	if _, err := pool.Exec(ctx, `UPDATE auction_rounds SET started_at = now() - interval '2 hours'`); err != nil {
		t.Fatal(err)
	}
	if _, created, err := item.RefreshIfDue(ctx, pool, cfg, nil); err != nil || !created {
		t.Fatalf("aged round: created=%v err=%v", created, err)
	}
	var rounds int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM auction_rounds`).Scan(&rounds); err != nil {
		t.Fatal(err)
	}
	if rounds != 2 {
		t.Fatalf("%d rounds, want 2", rounds)
	}
	testutil.RequireInvariants(t, pool)
}

// Several workers polling at once must create a single round.
func TestRefreshIfDueIsSafeAcrossConcurrentWorkers(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	if _, err := item.SeedTemplates(ctx, db.New(pool)); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.RefreshInterval = 3_600_000_000_000 // 1h

	var created atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, ok, err := item.RefreshIfDue(ctx, pool, cfg, nil)
			if err != nil {
				t.Error(err)
			}
			if ok {
				created.Add(1)
			}
		}()
	}
	wg.Wait()
	if created.Load() != 1 {
		t.Fatalf("%d workers created a round, want exactly 1", created.Load())
	}
	var rounds int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM auction_rounds`).Scan(&rounds); err != nil {
		t.Fatal(err)
	}
	if rounds != 1 {
		t.Fatalf("%d rounds, want 1", rounds)
	}
}
