// Command seed inserts the development data set: six players with 10000 gold
// each (granted through the ledger), the item templates and one auction round.
// It is a no-op when players already exist.
package main

import (
	"context"
	"fmt"
	"log"

	"auctionhouse/internal/config"
	"auctionhouse/internal/db"
	"auctionhouse/internal/item"
	"auctionhouse/internal/wallet"
)

type player struct {
	name  string
	level int32
}

var players = []player{
	{"阿爾文", 5},
	{"貝拉", 14},
	{"卡爾", 23},
	{"黛安", 24},
	{"艾倫", 37},
	{"菲歐娜", 48},
}

const startingGold = 10000

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

	var seeded bool
	err = db.InTx(ctx, pool, func(q *db.Queries) error {
		n, err := q.CountUsers(ctx)
		if err != nil {
			return err
		}
		if n > 0 {
			return nil
		}
		for _, p := range players {
			u, err := q.CreateUser(ctx, db.CreateUserParams{Name: p.name, Level: p.level})
			if err != nil {
				return fmt.Errorf("create user %s: %w", p.name, err)
			}
			if err := wallet.Grant(ctx, q, u.ID, startingGold, u.ID); err != nil {
				return fmt.Errorf("grant gold to %s: %w", p.name, err)
			}
		}
		if _, err := item.SeedTemplates(ctx, q); err != nil {
			return err
		}
		seeded = true
		return nil
	})
	if err != nil {
		log.Fatalf("seed: %v", err)
	}
	if !seeded {
		log.Println("seed: players already exist, nothing to do")
		return
	}
	res, err := item.Refresh(ctx, pool, cfg, nil)
	if err != nil {
		log.Fatalf("seed refresh: %v", err)
	}
	log.Printf("seed: created %d players, %d templates, round %d with %d auctions",
		len(players), len(item.DefaultTemplates()), res.RoundID, res.Auctions)
}
