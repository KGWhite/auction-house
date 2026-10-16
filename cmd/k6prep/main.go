// Command k6prep prepares an isolated database for the k6 smoke test: it
// creates a scratch database, applies the migrations, and inserts the bidders
// and the one auction they will fight over. `create` prints shell `export`
// lines for k6/run-smoke.sh to eval; `drop NAME` removes the scratch database.
package main

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"auctionhouse/internal/config"
	"auctionhouse/internal/db"
)

const (
	bidders      = 50
	startingGold = 1_000_000_000_000
	startPrice   = 1000
	bracketLevel = 25 // bracket 2
)

func main() {
	log.SetFlags(0)
	if len(os.Args) < 2 {
		log.Fatal("usage: k6prep create | k6prep drop NAME")
	}
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
		log.Fatal(err)
	}
	defer admin.Close()

	switch os.Args[1] {
	case "create":
		create(ctx, cfg, admin)
	case "drop":
		if len(os.Args) != 3 || !strings.HasPrefix(os.Args[2], "k6_smoke_") {
			log.Fatal("usage: k6prep drop k6_smoke_XXXXXXXX")
		}
		if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+os.Args[2]+" WITH (FORCE)"); err != nil {
			log.Fatal(err)
		}
	default:
		log.Fatalf("unknown command %q", os.Args[1])
	}
}

func create(ctx context.Context, cfg config.Config, admin *pgxpool.Pool) {
	var b [4]byte
	_, _ = crand.Read(b[:])
	name := "k6_smoke_" + hex.EncodeToString(b[:])
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		log.Fatalf("create database: %v", err)
	}
	dsn, err := withDatabase(cfg.DatabaseURL, name)
	if err != nil {
		log.Fatal(err)
	}
	if err := db.Migrate(ctx, dsn); err != nil {
		log.Fatal(err)
	}
	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	must := func(err error) {
		if err != nil {
			log.Fatalf("prepare: %v", err)
		}
	}
	ids := make([]string, 0, bidders)
	for i := 1; i <= bidders; i++ {
		var id int64
		must(pool.QueryRow(ctx, `INSERT INTO users (name, level, gold_available) VALUES ($1, $2, $3) RETURNING id`,
			fmt.Sprintf("k6-user-%02d", i), bracketLevel, startingGold).Scan(&id))
		_, err := pool.Exec(ctx, `INSERT INTO gold_ledger (user_id, entry_type, delta_available, delta_frozen, ref_type, ref_id)
			VALUES ($1, 'grant', $2, 0, 'seed', $1)`, id, startingGold)
		must(err)
		ids = append(ids, fmt.Sprint(id))
	}
	var tmpl, item, round, auctionID int64
	must(pool.QueryRow(ctx, `INSERT INTO item_templates (name, type, base_value, min_level) VALUES ('Smoke Test Blade', 'weapon', $1, 1) RETURNING id`, startPrice).Scan(&tmpl))
	must(pool.QueryRow(ctx, `INSERT INTO items (template_id, level) VALUES ($1, $2) RETURNING id`, tmpl, bracketLevel).Scan(&item))
	must(pool.QueryRow(ctx, `INSERT INTO auction_rounds DEFAULT VALUES RETURNING id`).Scan(&round))
	must(pool.QueryRow(ctx, `INSERT INTO auctions (round_id, item_id, level_bracket, start_price, current_price, end_at)
		VALUES ($1, $2, $3, $4, $4, now() + interval '2 hours') RETURNING id`, round, item, bracketLevel/10, startPrice).Scan(&auctionID))

	fmt.Printf("export SMOKE_DB_NAME=%s\n", name)
	fmt.Printf("export SMOKE_DATABASE_URL='%s'\n", dsn)
	fmt.Printf("export SMOKE_AUCTION_ID=%d\n", auctionID)
	fmt.Printf("export SMOKE_USER_IDS=%s\n", strings.Join(ids, ","))
}

func withDatabase(rawURL, name string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	u.Path = "/" + name
	return u.String(), nil
}
