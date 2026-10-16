// Package testutil provides database fixtures for integration tests.
//
// By default a throwaway PostgreSQL 16 container is started with
// testcontainers-go. When TEST_DATABASE_URL is set (for environments without
// Docker) a fresh database is created on that server instead.
package testutil

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"auctionhouse/internal/db"
	"auctionhouse/internal/invariants"
)

var (
	once     sync.Once
	pool     *pgxpool.Pool
	dsn      string
	setupErr error
	cleanups []func()
)

// Run is meant for TestMain: it runs the tests and then tears down the
// database that Pool lazily created.
//
//	func TestMain(m *testing.M) { os.Exit(testutil.Run(m)) }
func Run(m *testing.M) int {
	code := m.Run()
	for i := len(cleanups) - 1; i >= 0; i-- {
		cleanups[i]()
	}
	return code
}

func setup() {
	ctx := context.Background()
	if adminURL := os.Getenv("TEST_DATABASE_URL"); adminURL != "" {
		dsn, setupErr = createDatabase(ctx, adminURL)
	} else {
		dsn, setupErr = startContainer(ctx)
	}
	if setupErr != nil {
		return
	}
	if setupErr = db.Migrate(ctx, dsn); setupErr != nil {
		return
	}
	pool, setupErr = db.Connect(ctx, dsn)
	if setupErr == nil {
		cleanups = append(cleanups, pool.Close)
	}
}

func startContainer(ctx context.Context) (string, error) {
	ctr, err := tcpostgres.Run(ctx, "postgres:16",
		tcpostgres.WithDatabase("auction_test"),
		tcpostgres.WithUsername("auction"),
		tcpostgres.WithPassword("auction"),
		testcontainers.WithCmd("postgres", "-c", "fsync=off", "-c", "max_connections=300"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(90*time.Second)),
	)
	if err != nil {
		return "", fmt.Errorf("start postgres container (set TEST_DATABASE_URL to use an existing server): %w", err)
	}
	cleanups = append(cleanups, func() { _ = testcontainers.TerminateContainer(ctr) })
	return ctr.ConnectionString(ctx, "sslmode=disable")
}

func createDatabase(ctx context.Context, adminURL string) (string, error) {
	admin, err := db.Connect(ctx, adminURL)
	if err != nil {
		return "", err
	}
	defer admin.Close()
	var b [4]byte
	_, _ = rand.Read(b[:])
	name := fmt.Sprintf("ah_test_%d_%s", os.Getpid(), hex.EncodeToString(b[:]))
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		return "", fmt.Errorf("create test database: %w", err)
	}
	cleanups = append(cleanups, func() {
		c, err := db.Connect(context.Background(), adminURL)
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = c.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})
	u, err := url.Parse(adminURL)
	if err != nil {
		return "", err
	}
	u.Path = "/" + name
	return u.String(), nil
}

// Pool returns a pool on the shared, migrated test database after truncating
// every table, so each test starts from an empty schema. Tests in one package
// run sequentially, so a shared database is safe.
func Pool(t testing.TB) *pgxpool.Pool {
	t.Helper()
	once.Do(setup)
	if setupErr != nil {
		t.Fatalf("test database setup failed: %v", setupErr)
	}
	_, err := pool.Exec(context.Background(),
		`TRUNCATE inbox, gold_ledger, bids, auctions, auction_rounds, items, item_templates, users RESTART IDENTITY CASCADE`)
	if err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return pool
}

// DSN returns the connection string of the shared test database (valid after Pool).
func DSN() string { return dsn }

// RequireInvariants fails the test if any ledger invariant is violated.
func RequireInvariants(t testing.TB, p *pgxpool.Pool) {
	t.Helper()
	vs, err := invariants.CheckInvariants(context.Background(), p)
	if err != nil {
		t.Fatalf("CheckInvariants: %v", err)
	}
	for _, v := range vs {
		t.Errorf("invariant violated: %s", v)
	}
	if len(vs) > 0 {
		t.FailNow()
	}
}

var seq atomic.Int64

// NewUser inserts a player with the given level and grants gold through a
// 'grant' ledger entry, exactly as the seed does.
func NewUser(t testing.TB, p *pgxpool.Pool, level int, gold int64) int64 {
	t.Helper()
	ctx := context.Background()
	name := fmt.Sprintf("player-%d", seq.Add(1))
	var id int64
	if err := p.QueryRow(ctx, `INSERT INTO users (name, level) VALUES ($1, $2) RETURNING id`, name, level).Scan(&id); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if gold > 0 {
		if _, err := p.Exec(ctx, `UPDATE users SET gold_available = $2 WHERE id = $1`, id, gold); err != nil {
			t.Fatalf("grant gold: %v", err)
		}
		if _, err := p.Exec(ctx, `INSERT INTO gold_ledger (user_id, entry_type, delta_available, delta_frozen, ref_type, ref_id)
			VALUES ($1, 'grant', $2, 0, 'seed', $1)`, id, gold); err != nil {
			t.Fatalf("grant ledger: %v", err)
		}
	}
	return id
}

// AuctionOpts configures NewAuction. Zero values pick sensible defaults.
type AuctionOpts struct {
	ItemLevel  int           // default 15
	StartPrice int64         // default 1000
	Duration   time.Duration // end_at = now() + Duration; default 10m, may be negative
}

// NewAuction inserts a template, an item and an active auction and returns the
// auction id. The bracket is derived from the item level.
func NewAuction(t testing.TB, p *pgxpool.Pool, o AuctionOpts) int64 {
	t.Helper()
	if o.ItemLevel == 0 {
		o.ItemLevel = 15
	}
	if o.StartPrice == 0 {
		o.StartPrice = 1000
	}
	if o.Duration == 0 {
		o.Duration = 10 * time.Minute
	}
	ctx := context.Background()
	n := seq.Add(1)
	var tmpl, item, round, id int64
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("fixture: %v", err)
		}
	}
	must(p.QueryRow(ctx, `INSERT INTO item_templates (name, type, base_value, min_level) VALUES ($1, 'weapon', $2, 1) RETURNING id`,
		fmt.Sprintf("test-item-%d", n), o.StartPrice).Scan(&tmpl))
	must(p.QueryRow(ctx, `INSERT INTO items (template_id, level) VALUES ($1, $2) RETURNING id`, tmpl, o.ItemLevel).Scan(&item))
	must(p.QueryRow(ctx, `INSERT INTO auction_rounds DEFAULT VALUES RETURNING id`).Scan(&round))
	must(p.QueryRow(ctx, `INSERT INTO auctions (round_id, item_id, level_bracket, start_price, current_price, end_at)
		VALUES ($1, $2, $3, $4, $4, now() + $5::bigint * interval '1 millisecond') RETURNING id`,
		round, item, o.ItemLevel/10, o.StartPrice, o.Duration.Milliseconds()).Scan(&id))
	return id
}

// SetEndIn moves an auction's end time to now() + d (database clock).
func SetEndIn(t testing.TB, p *pgxpool.Pool, auctionID int64, d time.Duration) {
	t.Helper()
	if _, err := p.Exec(context.Background(),
		`UPDATE auctions SET end_at = now() + $2::bigint * interval '1 millisecond' WHERE id = $1`,
		auctionID, d.Milliseconds()); err != nil {
		t.Fatalf("set end: %v", err)
	}
}
