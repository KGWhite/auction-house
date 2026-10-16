// Package invariants verifies the bookkeeping guarantees of the auction house.
// The same implementation backs the concurrency tests and cmd/reconcile.
package invariants

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"auctionhouse/internal/db"
)

// Violation codes, one per rule in the specification.
const (
	CodeBalanceMismatch = "a_balance_ledger_mismatch"
	CodeNegativeBalance = "b_negative_balance"
	CodeConservation    = "c_gold_not_conserved"
	CodeActiveNonTop    = "d_active_non_top_frozen"
	CodeActiveTopFrozen = "d_active_top_frozen_mismatch"
	CodeActiveNoBidder  = "d_active_no_bidder_price"
	CodeSettledAuction  = "e_settled_auction"
	CodeOrphanSpend     = "e_orphan_spend"
)

// Violation describes one broken invariant.
type Violation struct {
	Code    string
	Message string
}

func (v Violation) String() string { return fmt.Sprintf("[%s] %s", v.Code, v.Message) }

// CheckInvariants evaluates every invariant against a single consistent
// snapshot (REPEATABLE READ, read-only) and returns all violations found. An
// empty slice means everything holds. It is safe to run while the system is
// processing bids.
//
//	a. every user's gold_available / gold_frozen equal their ledger sums
//	b. no negative balances
//	c. conservation: sum(available + frozen) + sum(spend) == sum(grant)
//	d. every active auction: only the top bidder has outstanding frozen gold
//	   for it, and that amount equals current_price
//	e. every settled auction: item owner is the top bidder and exactly one
//	   spend ledger entry equals the final price
func CheckInvariants(ctx context.Context, pool *pgxpool.Pool) ([]Violation, error) {
	var out []Violation
	err := pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var err error
		out, err = check(ctx, db.New(tx))
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("check invariants: %w", err)
	}
	return out, nil
}

func check(ctx context.Context, q *db.Queries) ([]Violation, error) {
	var out []Violation
	add := func(code, format string, args ...any) {
		out = append(out, Violation{Code: code, Message: fmt.Sprintf(format, args...)})
	}

	mismatches, err := q.InvBalanceMismatches(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range mismatches {
		add(CodeBalanceMismatch, "user %d: balance available=%d frozen=%d but ledger sums available=%d frozen=%d",
			r.UserID, r.GoldAvailable, r.GoldFrozen, r.LedgerAvailable, r.LedgerFrozen)
	}

	negatives, err := q.InvNegativeBalances(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range negatives {
		add(CodeNegativeBalance, "user %d has negative balance: available=%d frozen=%d", r.UserID, r.GoldAvailable, r.GoldFrozen)
	}

	c, err := q.InvConservation(ctx)
	if err != nil {
		return nil, err
	}
	if c.TotalHeld+c.TotalSpent != c.TotalGranted {
		add(CodeConservation, "held(%d) + spent(%d) = %d, but granted = %d",
			c.TotalHeld, c.TotalSpent, c.TotalHeld+c.TotalSpent, c.TotalGranted)
	}

	nonTop, err := q.InvActiveNonTopFrozen(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range nonTop {
		add(CodeActiveNonTop, "active auction %d: user %d is not the top bidder but still has %d frozen for it",
			r.AuctionID, r.UserID, r.NetFrozen)
	}

	top, err := q.InvActiveTopFrozen(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range top {
		add(CodeActiveTopFrozen, "active auction %d: top bidder %d has %d frozen but current_price is %d",
			r.AuctionID, r.UserID, r.NetFrozen, r.CurrentPrice)
	}

	noBidder, err := q.InvActiveNoBidderPrice(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range noBidder {
		add(CodeActiveNoBidder, "active auction %d has no top bidder but current_price %d != start_price %d",
			r.AuctionID, r.CurrentPrice, r.StartPrice)
	}

	settled, err := q.InvSettledViolations(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range settled {
		add(CodeSettledAuction, "settled auction %d: top_bidder=%s item_owner=%s price=%d spend_entries=%d spend_total=%d",
			r.AuctionID, ptr(r.TopBidderID), ptr(r.OwnerID), r.CurrentPrice, r.SpendCount, r.SpendTotal)
	}

	orphans, err := q.InvOrphanSpends(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range orphans {
		add(CodeOrphanSpend, "spend ledger entry %d (user %d) references auction %d which is not settled", r.LedgerID, r.UserID, r.AuctionID)
	}
	return out, nil
}

func ptr(p *int64) string {
	if p == nil {
		return "none"
	}
	return fmt.Sprint(*p)
}
