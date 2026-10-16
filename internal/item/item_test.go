package item_test

import (
	"context"
	"math/rand/v2"
	"os"
	"testing"
	"time"

	"auctionhouse/internal/auction"
	"auctionhouse/internal/config"
	"auctionhouse/internal/db"
	"auctionhouse/internal/item"
	"auctionhouse/internal/testutil"
)

func TestMain(m *testing.M) { os.Exit(testutil.Run(m)) }

func TestDefaultTemplates(t *testing.T) {
	tmpls := item.DefaultTemplates()
	if len(tmpls) < 24 {
		t.Fatalf("need at least 24 templates, got %d", len(tmpls))
	}
	types := map[string]bool{}
	minL, maxL := 1000, 0
	for _, tm := range tmpls {
		types[tm.Type] = true
		minL, maxL = min(minL, tm.MinLevel), max(maxL, tm.MinLevel)
		if tm.BaseValue <= 0 || tm.Name == "" {
			t.Errorf("bad template %+v", tm)
		}
	}
	for _, typ := range item.Types {
		if !types[typ] {
			t.Errorf("type %s not covered", typ)
		}
	}
	if minL != 1 || maxL != 50 {
		t.Errorf("min_level range [%d,%d] should span 1..50", minL, maxL)
	}
}

func TestSeedTemplatesIsIdempotent(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	q := db.New(pool)
	n, err := item.SeedTemplates(ctx, q)
	if err != nil || n != len(item.DefaultTemplates()) {
		t.Fatalf("first seed: n=%d err=%v", n, err)
	}
	n, err = item.SeedTemplates(ctx, q)
	if err != nil || n != 0 {
		t.Fatalf("second seed should insert nothing: n=%d err=%v", n, err)
	}
}

func TestRefreshCreatesRoundPerBracket(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	if _, err := item.SeedTemplates(ctx, db.New(pool)); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	rng := rand.New(rand.NewPCG(1, 2))

	res, err := item.Refresh(ctx, pool, cfg, rng)
	if err != nil {
		t.Fatal(err)
	}
	wantAuctions := (cfg.BracketMax - cfg.BracketMin + 1) * cfg.ItemsPerBracket
	if res.Auctions != wantAuctions {
		t.Fatalf("auctions = %d, want %d", res.Auctions, wantAuctions)
	}

	rows, err := pool.Query(ctx, `
		SELECT a.level_bracket, i.level, t.min_level, t.base_value, a.start_price, a.current_price,
		       a.status, a.top_bidder_id IS NULL,
		       EXTRACT(EPOCH FROM (a.end_at - a.created_at)) * 1000
		FROM auctions a JOIN items i ON i.id = a.item_id JOIN item_templates t ON t.id = i.template_id
		WHERE a.round_id = $1`, res.RoundID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	perBracket := map[int]int{}
	for rows.Next() {
		var bracket, level, minLevel int
		var base, start, cur int64
		var status string
		var noBidder bool
		var durMs float64
		if err := rows.Scan(&bracket, &level, &minLevel, &base, &start, &cur, &status, &noBidder, &durMs); err != nil {
			t.Fatal(err)
		}
		perBracket[bracket]++
		if auction.Bracket(level) != bracket {
			t.Errorf("item level %d outside bracket %d", level, bracket)
		}
		if level < minLevel || level < 1 {
			t.Errorf("item level %d below template min_level %d", level, minLevel)
		}
		if start != auction.StartPrice(base, level) || cur != start {
			t.Errorf("price: start=%d current=%d want %d", start, cur, auction.StartPrice(base, level))
		}
		if status != "active" || !noBidder {
			t.Errorf("new auction must be active without bidder")
		}
		d := time.Duration(durMs * float64(time.Millisecond))
		if d < cfg.AuctionMinDuration-time.Second || d > cfg.AuctionMaxDuration+time.Second {
			t.Errorf("duration %v outside [%v,%v]", d, cfg.AuctionMinDuration, cfg.AuctionMaxDuration)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for b := cfg.BracketMin; b <= cfg.BracketMax; b++ {
		if perBracket[b] != cfg.ItemsPerBracket {
			t.Errorf("bracket %d has %d auctions, want %d", b, perBracket[b], cfg.ItemsPerBracket)
		}
	}
	testutil.RequireInvariants(t, pool)
}

func TestRefreshDurationsAreSpread(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	if _, err := item.SeedTemplates(ctx, db.New(pool)); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.ItemsPerBracket = 30
	if _, err := item.Refresh(ctx, pool, cfg, nil); err != nil {
		t.Fatal(err)
	}
	var minD, maxD float64
	if err := pool.QueryRow(ctx, `SELECT MIN(EXTRACT(EPOCH FROM (end_at - created_at))), MAX(EXTRACT(EPOCH FROM (end_at - created_at))) FROM auctions`).Scan(&minD, &maxD); err != nil {
		t.Fatal(err)
	}
	if maxD-minD < 5*60 { // 180 auctions spread over a 22 minute window
		t.Fatalf("durations not spread: min=%vs max=%vs", minD, maxD)
	}
}
