// Command demo-race is a teaching demo. It implements bidding the WRONG way on
// purpose: no transaction, no row locks, and read-then-write with values
// computed from stale reads. It then runs many concurrent bidders against one
// auction and prints every ledger invariant that got broken.
//
// It runs against a throwaway database that it creates and drops itself, so
// the development data is never touched. Nothing in the production code may
// import this package.
package main

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"net/url"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"auctionhouse/internal/config"
	"auctionhouse/internal/db"
	"auctionhouse/internal/invariants"
)

const (
	bidders         = 30
	bidsPerBidder   = 15
	startingGold    = 1_000_000
	startPrice      = 1000
	raceWindowMaxMs = 3 // pause between the stale read and the write, to widen the race
)

func main() {
	log.SetFlags(0)
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()

	adminURL, err := withDatabase(cfg.DatabaseURL, "postgres")
	if err != nil {
		log.Fatal(err)
	}
	admin, err := db.Connect(ctx, adminURL)
	if err != nil {
		log.Fatalf("cannot reach the database server: %v", err)
	}
	defer admin.Close()

	var suffix [4]byte
	_, _ = crand.Read(suffix[:])
	name := "demo_race_" + hex.EncodeToString(suffix[:])
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		log.Fatalf("create scratch database: %v", err)
	}
	defer func() {
		if _, err := admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			log.Printf("could not drop scratch database %s: %v", name, err)
		}
	}()

	dsn, err := withDatabase(cfg.DatabaseURL, name)
	if err != nil {
		log.Fatal(err)
	}
	if err := db.Migrate(ctx, dsn); err != nil {
		log.Fatal(err)
	}
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		log.Fatal(err)
	}
	poolCfg.MaxConns = bidders + 5
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	users, auctionID := setup(ctx, pool)

	fmt.Printf("demo-race: %d bidders x %d bids on ONE auction using an UNSAFE read-then-write implementation\n", bidders, bidsPerBidder)
	fmt.Println("           (no transaction, no row locks, balances written from stale reads)")

	stats := runRace(ctx, pool, users, auctionID)
	report(ctx, pool, auctionID, stats)
}

type raceStats struct {
	attempted, accepted, rejected, errored atomic.Int64
	maxAccepted                            atomic.Int64
	firstErrors                            sync.Map // message -> count (as *atomic.Int64)
}

func (s *raceStats) noteError(err error) {
	s.errored.Add(1)
	c, _ := s.firstErrors.LoadOrStore(err.Error(), new(atomic.Int64))
	c.(*atomic.Int64).Add(1)
}

func runRace(ctx context.Context, pool *pgxpool.Pool, users []int64, auctionID int64) *raceStats {
	stats := &raceStats{}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, uid := range users {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < bidsPerBidder; i++ {
				stats.attempted.Add(1)
				amount, err := unsafeBid(ctx, pool, uid, auctionID, fmt.Sprintf("u%d-%d", uid, i))
				switch {
				case err == nil:
					stats.accepted.Add(1)
					for {
						cur := stats.maxAccepted.Load()
						if amount <= cur || stats.maxAccepted.CompareAndSwap(cur, amount) {
							break
						}
					}
				case errors.Is(err, errRejected):
					stats.rejected.Add(1)
				default:
					stats.noteError(err)
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	return stats
}

var errRejected = errors.New("rejected by a business rule")

// unsafeBid is deliberately wrong. Compare with auction.Service.PlaceBid.
func unsafeBid(ctx context.Context, pool *pgxpool.Pool, userID, auctionID int64, key string) (int64, error) {
	// READ (no lock): current state of the auction.
	var price, start int64
	var top *int64
	if err := pool.QueryRow(ctx, `SELECT current_price, start_price, top_bidder_id FROM auctions WHERE id=$1`, auctionID).Scan(&price, &start, &top); err != nil {
		return 0, err
	}
	if top != nil && *top == userID {
		return 0, errRejected
	}
	minNext := int64(startPrice)
	if top != nil {
		minNext = price + max((price*5+99)/100, 10)
	}
	amount := minNext + rand.Int64N(40)

	// READ (no lock): our balance.
	var avail, frozen int64
	if err := pool.QueryRow(ctx, `SELECT gold_available, gold_frozen FROM users WHERE id=$1`, userID).Scan(&avail, &frozen); err != nil {
		return 0, err
	}
	if avail < amount {
		return 0, errRejected
	}

	// READ (no lock): who we are about to outbid.
	var prevBidID, prevAmount, prevUser int64
	hasPrev := false
	if top != nil {
		err := pool.QueryRow(ctx, `SELECT id, amount, user_id FROM bids WHERE auction_id=$1 ORDER BY id DESC LIMIT 1`, auctionID).Scan(&prevBidID, &prevAmount, &prevUser)
		if err == nil {
			hasPrev = true
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return 0, err
		}
	}

	// The window in which every other bidder can change what we just read.
	time.Sleep(time.Duration(rand.IntN(raceWindowMaxMs*1000)) * time.Microsecond)

	var bidID int64
	if err := pool.QueryRow(ctx, `INSERT INTO bids (auction_id, user_id, amount, idempotency_key) VALUES ($1,$2,$3,$4) RETURNING id`,
		auctionID, userID, amount, key).Scan(&bidID); err != nil {
		return 0, err
	}

	// WRITE absolute values computed from the stale read: a lost update waiting to happen.
	if _, err := pool.Exec(ctx, `UPDATE users SET gold_available=$2, gold_frozen=$3 WHERE id=$1`, userID, avail-amount, frozen+amount); err != nil {
		return 0, err
	}
	if _, err := pool.Exec(ctx, `INSERT INTO gold_ledger (user_id, entry_type, delta_available, delta_frozen, ref_type, ref_id) VALUES ($1,'freeze',$2,$3,'bid',$4)`,
		userID, -amount, amount, bidID); err != nil {
		return 0, err
	}

	if hasPrev {
		// Refund the previous top bidder, again from a stale read of their balance.
		var pAvail, pFrozen int64
		if err := pool.QueryRow(ctx, `SELECT gold_available, gold_frozen FROM users WHERE id=$1`, prevUser).Scan(&pAvail, &pFrozen); err != nil {
			return 0, err
		}
		time.Sleep(time.Duration(rand.IntN(raceWindowMaxMs*1000)) * time.Microsecond)
		if _, err := pool.Exec(ctx, `UPDATE users SET gold_available=$2, gold_frozen=$3 WHERE id=$1`, prevUser, pAvail+prevAmount, pFrozen-prevAmount); err != nil {
			return 0, err
		}
		// Two bidders that both saw the same previous leader refund them twice; the
		// ledger's unique key rejects the second entry, but the balance already moved.
		_, _ = pool.Exec(ctx, `INSERT INTO gold_ledger (user_id, entry_type, delta_available, delta_frozen, ref_type, ref_id) VALUES ($1,'unfreeze',$2,$3,'bid',$4)`,
			prevUser, prevAmount, -prevAmount, prevBidID)
	}

	if _, err := pool.Exec(ctx, `UPDATE auctions SET current_price=$2, top_bidder_id=$3 WHERE id=$1`, auctionID, amount, userID); err != nil {
		return 0, err
	}
	return amount, nil
}

func setup(ctx context.Context, pool *pgxpool.Pool) ([]int64, int64) {
	must := func(err error) {
		if err != nil {
			log.Fatalf("setup: %v", err)
		}
	}
	var users []int64
	for i := 0; i < bidders; i++ {
		var id int64
		must(pool.QueryRow(ctx, `INSERT INTO users (name, level, gold_available) VALUES ($1, 15, $2) RETURNING id`, fmt.Sprintf("racer-%02d", i), startingGold).Scan(&id))
		_, err := pool.Exec(ctx, `INSERT INTO gold_ledger (user_id, entry_type, delta_available, delta_frozen, ref_type, ref_id) VALUES ($1,'grant',$2,0,'seed',$1)`, id, startingGold)
		must(err)
		users = append(users, id)
	}
	var tmpl, item, round, auctionID int64
	must(pool.QueryRow(ctx, `INSERT INTO item_templates (name, type, base_value, min_level) VALUES ('Demo Sword', 'weapon', $1, 1) RETURNING id`, startPrice).Scan(&tmpl))
	must(pool.QueryRow(ctx, `INSERT INTO items (template_id, level) VALUES ($1, 15) RETURNING id`, tmpl).Scan(&item))
	must(pool.QueryRow(ctx, `INSERT INTO auction_rounds DEFAULT VALUES RETURNING id`).Scan(&round))
	must(pool.QueryRow(ctx, `INSERT INTO auctions (round_id, item_id, level_bracket, start_price, current_price, end_at)
		VALUES ($1, $2, 1, $3, $3, now() + interval '1 hour') RETURNING id`, round, item, startPrice).Scan(&auctionID))
	return users, auctionID
}

func report(ctx context.Context, pool *pgxpool.Pool, auctionID int64, s *raceStats) {
	var price int64
	var top *int64
	if err := pool.QueryRow(ctx, `SELECT current_price, top_bidder_id FROM auctions WHERE id=$1`, auctionID).Scan(&price, &top); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("\nbids attempted=%d accepted=%d rejected=%d failed=%d\n", s.attempted.Load(), s.accepted.Load(), s.rejected.Load(), s.errored.Load())
	fmt.Printf("highest accepted bid = %d, auction current_price = %d", s.maxAccepted.Load(), price)
	if price != s.maxAccepted.Load() {
		fmt.Printf("   <-- the auction does not show the highest bid!")
	}
	fmt.Println()
	s.firstErrors.Range(func(k, v any) bool {
		fmt.Printf("  error x%d: %v\n", v.(*atomic.Int64).Load(), k)
		return true
	})

	violations, err := invariants.CheckInvariants(ctx, pool)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println()
	if len(violations) == 0 {
		fmt.Println("no invariant was broken this time (rare: re-run to see the race)")
		return
	}
	byCode := map[string][]invariants.Violation{}
	for _, v := range violations {
		byCode[v.Code] = append(byCode[v.Code], v)
	}
	codes := make([]string, 0, len(byCode))
	for c := range byCode {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	fmt.Printf("BROKEN INVARIANTS: %d violation(s) in %d rule(s)\n", len(violations), len(codes))
	for _, c := range codes {
		vs := byCode[c]
		fmt.Printf("\n  %s: %d violation(s)\n", c, len(vs))
		for i, v := range vs {
			if i == 3 {
				fmt.Printf("    ... and %d more\n", len(vs)-3)
				break
			}
			fmt.Printf("    - %s\n", v.Message)
		}
	}
	fmt.Println("\nThe safe implementation (auction.Service.PlaceBid) locks the auction row, then the user rows in")
	fmt.Println("ascending id order, and does everything in one transaction, so none of this can happen.")
}

func withDatabase(rawURL, name string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	u.Path = "/" + name
	return u.String(), nil
}
