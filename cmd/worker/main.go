// Command worker runs the background jobs: settling ended auctions every
// SETTLE_INTERVAL and creating a new auction round every REFRESH_INTERVAL.
// Several workers may run at once; both jobs are safe under concurrency.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"auctionhouse/internal/auction"
	"auctionhouse/internal/config"
	"auctionhouse/internal/db"
	"auctionhouse/internal/item"
)

// How often the refresh job checks whether a round is due. The decision itself
// is made by the database against REFRESH_INTERVAL.
const refreshPoll = time.Second

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("worker: ")
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	svc := auction.NewService(pool, auction.RulesFromConfig(cfg))
	log.Printf("started: settle every %s (batch %d), refresh every %s", cfg.SettleInterval, cfg.SettleBatchSize, cfg.RefreshInterval)

	done := make(chan struct{}, 2)
	go func() {
		defer func() { done <- struct{}{} }()
		settleLoop(ctx, svc, cfg)
	}()
	go func() {
		defer func() { done <- struct{}{} }()
		refreshLoop(ctx, pool, cfg)
	}()
	<-ctx.Done()
	<-done
	<-done
	log.Println("stopped")
}

func settleLoop(ctx context.Context, svc *auction.Service, cfg config.Config) {
	ticker := time.NewTicker(cfg.SettleInterval)
	defer ticker.Stop()
	for {
		// Drain: keep going while full batches come back.
		for ctx.Err() == nil {
			stats, err := svc.SettleDue(ctx, cfg.SettleBatchSize)
			if err != nil && ctx.Err() == nil {
				log.Printf("settle: %v", err)
			}
			if stats.Settled+stats.Expired > 0 {
				log.Printf("settled %d, expired %d, skipped %d", stats.Settled, stats.Expired, stats.Skipped)
			}
			if err != nil || stats.Found < cfg.SettleBatchSize {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func refreshLoop(ctx context.Context, pool *pgxpool.Pool, cfg config.Config) {
	ticker := time.NewTicker(refreshPoll)
	defer ticker.Stop()
	for {
		res, created, err := item.RefreshIfDue(ctx, pool, cfg, nil)
		switch {
		case err != nil && ctx.Err() == nil:
			log.Printf("refresh: %v", err)
		case created:
			log.Printf("refresh: round %d with %d auctions", res.RoundID, res.Auctions)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
