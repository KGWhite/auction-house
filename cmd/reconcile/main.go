// Command reconcile checks the ledger invariants against the current database.
// It exits with status 1 when any invariant is violated. It is safe to run at
// any time, including while the server is handling bids: the check reads a
// single consistent snapshot.
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"auctionhouse/internal/config"
	"auctionhouse/internal/db"
	"auctionhouse/internal/invariants"
)

func main() {
	log.SetFlags(0)
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	violations, err := invariants.CheckInvariants(ctx, pool)
	if err != nil {
		log.Fatal(err)
	}

	if len(violations) == 0 {
		fmt.Println("reconcile: OK - all invariants hold")
		fmt.Println("  a. balances equal ledger sums")
		fmt.Println("  b. no negative balances")
		fmt.Println("  c. gold is conserved (available + frozen + spent == granted)")
		fmt.Println("  d. active auctions: only the top bidder has frozen gold, equal to the current price")
		fmt.Println("  e. settled auctions: item owned by the winner, exactly one spend entry")
		return
	}
	fmt.Printf("reconcile: FAILED - %d violation(s)\n", len(violations))
	for _, v := range violations {
		fmt.Println("  " + v.String())
	}
	os.Exit(1)
}
