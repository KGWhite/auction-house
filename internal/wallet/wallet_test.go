package wallet_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"auctionhouse/internal/db"
	"auctionhouse/internal/testutil"
	"auctionhouse/internal/wallet"
)

func TestMain(m *testing.M) { os.Exit(testutil.Run(m)) }

// newEmptyUser creates a user with zero gold and no ledger history.
func newEmptyUser(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	return testutil.NewUser(t, pool, 10, 0)
}

type balance struct{ avail, frozen int64 }

func getBalance(t *testing.T, pool *pgxpool.Pool, uid int64) balance {
	t.Helper()
	var b balance
	if err := pool.QueryRow(context.Background(), `SELECT gold_available, gold_frozen FROM users WHERE id=$1`, uid).Scan(&b.avail, &b.frozen); err != nil {
		t.Fatal(err)
	}
	return b
}

type entry struct {
	typ          string
	dAvail, dFrz int64
	refType      string
	refID        int64
}

func ledger(t *testing.T, pool *pgxpool.Pool, uid int64) []entry {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT entry_type, delta_available, delta_frozen, ref_type, ref_id FROM gold_ledger WHERE user_id=$1 ORDER BY id`, uid)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.typ, &e.dAvail, &e.dFrz, &e.refType, &e.refID); err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

func TestNonPositiveAmountsRejectedWithoutTouchingDB(t *testing.T) {
	ctx := context.Background()
	for _, amt := range []int64{0, -5} {
		if err := wallet.Grant(ctx, nil, 1, amt, 1); !errors.Is(err, wallet.ErrInvalidAmount) {
			t.Errorf("Grant(%d): %v", amt, err)
		}
		if err := wallet.Freeze(ctx, nil, 1, amt, 1); !errors.Is(err, wallet.ErrInvalidAmount) {
			t.Errorf("Freeze(%d): %v", amt, err)
		}
		if err := wallet.Unfreeze(ctx, nil, 1, amt, 1); !errors.Is(err, wallet.ErrInvalidAmount) {
			t.Errorf("Unfreeze(%d): %v", amt, err)
		}
		if err := wallet.Spend(ctx, nil, 1, amt, 1); !errors.Is(err, wallet.ErrInvalidAmount) {
			t.Errorf("Spend(%d): %v", amt, err)
		}
	}
}

func TestGrantFreezeUnfreezeSpendLedgerShapes(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	uid := newEmptyUser(t, pool)

	step := func(name string, fn func(q *db.Queries) error, want balance) {
		t.Helper()
		if err := db.InTx(ctx, pool, fn); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := getBalance(t, pool, uid); got != want {
			t.Fatalf("%s: balance %+v, want %+v", name, got, want)
		}
	}
	step("grant", func(q *db.Queries) error { return wallet.Grant(ctx, q, uid, 1000, 7) }, balance{1000, 0})
	step("freeze", func(q *db.Queries) error { return wallet.Freeze(ctx, q, uid, 400, 11) }, balance{600, 400})
	step("unfreeze", func(q *db.Queries) error { return wallet.Unfreeze(ctx, q, uid, 400, 11) }, balance{1000, 0})
	step("freeze again", func(q *db.Queries) error { return wallet.Freeze(ctx, q, uid, 250, 12) }, balance{750, 250})
	step("spend", func(q *db.Queries) error { return wallet.Spend(ctx, q, uid, 250, 99) }, balance{750, 0})

	want := []entry{
		{"grant", 1000, 0, "seed", 7},
		{"freeze", -400, 400, "bid", 11},
		{"unfreeze", 400, -400, "bid", 11},
		{"freeze", -250, 250, "bid", 12},
		{"spend", 0, -250, "auction", 99},
	}
	got := ledger(t, pool, uid)
	if len(got) != len(want) {
		t.Fatalf("ledger has %d entries, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	// Invariants are not checked here: the spend references auction 99, which
	// does not exist, and CheckInvariants rightly reports that as an orphan spend.
}

func TestFreezeInsufficientGold(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	uid := testutil.NewUser(t, pool, 10, 100)

	err := db.InTx(ctx, pool, func(q *db.Queries) error { return wallet.Freeze(ctx, q, uid, 101, 1) })
	if !errors.Is(err, wallet.ErrInsufficientGold) {
		t.Fatalf("got %v, want ErrInsufficientGold", err)
	}
	if b := getBalance(t, pool, uid); b != (balance{100, 0}) {
		t.Fatalf("balance changed: %+v", b)
	}
	if n := len(ledger(t, pool, uid)); n != 1 { // only the grant
		t.Fatalf("failed freeze left %d ledger entries", n)
	}

	// Exactly the available amount is fine.
	if err := db.InTx(ctx, pool, func(q *db.Queries) error { return wallet.Freeze(ctx, q, uid, 100, 1) }); err != nil {
		t.Fatal(err)
	}
	testutil.RequireInvariants(t, pool)
}

func TestUnfreezeAndSpendNeedFrozenGold(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	uid := testutil.NewUser(t, pool, 10, 100)
	err := db.InTx(ctx, pool, func(q *db.Queries) error { return wallet.Unfreeze(ctx, q, uid, 10, 1) })
	if !errors.Is(err, wallet.ErrInsufficientFrozen) {
		t.Errorf("unfreeze: %v", err)
	}
	err = db.InTx(ctx, pool, func(q *db.Queries) error { return wallet.Spend(ctx, q, uid, 10, 1) })
	if !errors.Is(err, wallet.ErrInsufficientFrozen) {
		t.Errorf("spend: %v", err)
	}
}

func TestDuplicateOperationsRejected(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	uid := testutil.NewUser(t, pool, 10, 1000)

	if err := db.InTx(ctx, pool, func(q *db.Queries) error { return wallet.Freeze(ctx, q, uid, 100, 5) }); err != nil {
		t.Fatal(err)
	}
	err := db.InTx(ctx, pool, func(q *db.Queries) error { return wallet.Freeze(ctx, q, uid, 100, 5) })
	if !errors.Is(err, wallet.ErrDuplicateEntry) {
		t.Fatalf("second freeze for the same bid: %v", err)
	}
	// The failed transaction rolled back its balance update too.
	if b := getBalance(t, pool, uid); b != (balance{900, 100}) {
		t.Fatalf("balance %+v, want {900 100}", b)
	}
	testutil.RequireInvariants(t, pool)
}

func TestGrantUnknownUser(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	err := db.InTx(ctx, pool, func(q *db.Queries) error { return wallet.Grant(ctx, q, 424242, 10, 1) })
	if err == nil {
		t.Fatal("expected an error for an unknown user")
	}
}

func TestOperationAndLedgerAreAtomic(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	uid := testutil.NewUser(t, pool, 10, 500)
	boom := errors.New("boom")

	err := db.InTx(ctx, pool, func(q *db.Queries) error {
		if err := wallet.Freeze(ctx, q, uid, 200, 1); err != nil {
			return err
		}
		return boom // caller fails after the wallet call: everything must roll back
	})
	if !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
	if b := getBalance(t, pool, uid); b != (balance{500, 0}) {
		t.Fatalf("balance not rolled back: %+v", b)
	}
	if n := len(ledger(t, pool, uid)); n != 1 {
		t.Fatalf("ledger not rolled back: %d entries", n)
	}
	testutil.RequireInvariants(t, pool)
}

func TestLockUsers(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	a := newEmptyUser(t, pool)
	b := newEmptyUser(t, pool)

	// Duplicates and any order are accepted.
	if err := db.InTx(ctx, pool, func(q *db.Queries) error { return wallet.LockUsers(ctx, q, b, a, b) }); err != nil {
		t.Fatal(err)
	}
	err := db.InTx(ctx, pool, func(q *db.Queries) error { return wallet.LockUsers(ctx, q, a, 999999) })
	if !errors.Is(err, wallet.ErrUserNotFound) {
		t.Fatalf("got %v, want ErrUserNotFound", err)
	}
}

// Transactions that ask for the same two users in opposite orders must never
// deadlock, because LockUsers always locks in ascending id order.
func TestLockUsersOppositeOrderNeverDeadlocks(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	a := newEmptyUser(t, pool)
	b := newEmptyUser(t, pool)

	var wg sync.WaitGroup
	var failures atomic.Int64
	for i := 0; i < 200; i++ {
		first, second := a, b
		if i%2 == 1 {
			first, second = b, a
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := db.InTx(ctx, pool, func(q *db.Queries) error {
				return wallet.LockUsers(ctx, q, first, second)
			})
			if err != nil {
				failures.Add(1)
				t.Errorf("lock: %v", err)
			}
		}()
	}
	wg.Wait()
	if failures.Load() != 0 {
		t.Fatalf("%d lock transactions failed", failures.Load())
	}
}

// With a balance of 100 and 50 concurrent freezes of 10, exactly 10 can win.
func TestConcurrentFreezesNeverOverdraw(t *testing.T) {
	pool := testutil.Pool(t)
	ctx := context.Background()
	uid := testutil.NewUser(t, pool, 10, 100)

	var wg sync.WaitGroup
	var ok, insufficient atomic.Int64
	for i := 1; i <= 50; i++ {
		wg.Add(1)
		go func(bidID int64) {
			defer wg.Done()
			err := db.InTx(ctx, pool, func(q *db.Queries) error { return wallet.Freeze(ctx, q, uid, 10, bidID) })
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, wallet.ErrInsufficientGold):
				insufficient.Add(1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}(int64(i))
	}
	wg.Wait()
	if ok.Load() != 10 || insufficient.Load() != 40 {
		t.Fatalf("ok=%d insufficient=%d, want 10 and 40", ok.Load(), insufficient.Load())
	}
	if b := getBalance(t, pool, uid); b != (balance{0, 100}) {
		t.Fatalf("balance %+v, want {0 100}", b)
	}
	testutil.RequireInvariants(t, pool)
}
