// Package wallet owns every change to a player's gold. Each operation updates
// the balance and appends a ledger entry; callers run the functions on a
// Queries bound to their transaction so the balance update, the ledger entry
// and whatever business change triggered them commit or roll back together.
package wallet

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"auctionhouse/internal/db"
)

// Ledger entry types.
const (
	EntryGrant    = "grant"
	EntryFreeze   = "freeze"
	EntryUnfreeze = "unfreeze"
	EntrySpend    = "spend"
)

// Ledger reference types.
const (
	RefSeed    = "seed"
	RefBid     = "bid"
	RefAuction = "auction"
)

var (
	// ErrInvalidAmount is returned for zero or negative amounts.
	ErrInvalidAmount = errors.New("wallet: amount must be positive")
	// ErrInsufficientGold means gold_available is lower than the amount to freeze.
	ErrInsufficientGold = errors.New("wallet: insufficient available gold")
	// ErrInsufficientFrozen means gold_frozen is lower than the amount to release
	// or spend. It indicates a bookkeeping bug, never a normal user error.
	ErrInsufficientFrozen = errors.New("wallet: insufficient frozen gold")
	// ErrDuplicateEntry means the (user, entry type, reference) ledger entry
	// already exists, i.e. the same operation was applied twice.
	ErrDuplicateEntry = errors.New("wallet: duplicate ledger entry")
	// ErrUserNotFound is returned when the user row does not exist.
	ErrUserNotFound = errors.New("wallet: user not found")
)

// Grant credits gold_available. seq is the grant sequence number used as the
// ledger reference ('seed', seq).
func Grant(ctx context.Context, q *db.Queries, userID, amount, seq int64) error {
	if amount <= 0 {
		return ErrInvalidAmount
	}
	if err := insertEntry(ctx, q, userID, EntryGrant, amount, 0, RefSeed, seq); err != nil {
		return err
	}
	n, err := q.GrantGold(ctx, db.GrantGoldParams{UserID: userID, Amount: amount})
	if err != nil {
		return fmt.Errorf("grant gold: %w", err)
	}
	if n == 0 {
		return ErrUserNotFound
	}
	return nil
}

// Freeze moves amount from available to frozen for a bid. The conditional
// update fails with ErrInsufficientGold when the user cannot afford it.
func Freeze(ctx context.Context, q *db.Queries, userID, amount, bidID int64) error {
	if amount <= 0 {
		return ErrInvalidAmount
	}
	n, err := q.FreezeGold(ctx, db.FreezeGoldParams{UserID: userID, Amount: amount})
	if err != nil {
		return fmt.Errorf("freeze gold: %w", err)
	}
	if n == 0 {
		return ErrInsufficientGold
	}
	return insertEntry(ctx, q, userID, EntryFreeze, -amount, amount, RefBid, bidID)
}

// Unfreeze returns a previously frozen bid amount to available gold. bidID is
// the bid that was outbid.
func Unfreeze(ctx context.Context, q *db.Queries, userID, amount, bidID int64) error {
	if amount <= 0 {
		return ErrInvalidAmount
	}
	n, err := q.UnfreezeGold(ctx, db.UnfreezeGoldParams{UserID: userID, Amount: amount})
	if err != nil {
		return fmt.Errorf("unfreeze gold: %w", err)
	}
	if n == 0 {
		return ErrInsufficientFrozen
	}
	return insertEntry(ctx, q, userID, EntryUnfreeze, amount, -amount, RefBid, bidID)
}

// Spend consumes frozen gold when an auction is won.
func Spend(ctx context.Context, q *db.Queries, userID, amount, auctionID int64) error {
	if amount <= 0 {
		return ErrInvalidAmount
	}
	n, err := q.SpendFrozenGold(ctx, db.SpendFrozenGoldParams{UserID: userID, Amount: amount})
	if err != nil {
		return fmt.Errorf("spend gold: %w", err)
	}
	if n == 0 {
		return ErrInsufficientFrozen
	}
	return insertEntry(ctx, q, userID, EntrySpend, 0, -amount, RefAuction, auctionID)
}

// LockUsers takes row locks (SELECT ... FOR UPDATE) on the given users in
// ascending id order.
//
// The order is what prevents deadlocks. Two bids that involve the same pair of
// players (A outbids B on one auction while B outbids A on another) would
// otherwise lock the two user rows in opposite orders and wait for each other
// forever. If every transaction acquires user locks in ascending id order, a
// lock-wait cycle cannot form. Duplicate ids are locked once.
func LockUsers(ctx context.Context, q *db.Queries, ids ...int64) error {
	sorted := slices.Clone(ids)
	slices.Sort(sorted)
	sorted = slices.Compact(sorted)
	for _, id := range sorted {
		if _, err := q.GetUserForUpdate(ctx, id); err != nil {
			if isNoRows(err) {
				return ErrUserNotFound
			}
			return fmt.Errorf("lock user %d: %w", id, err)
		}
	}
	return nil
}

func insertEntry(ctx context.Context, q *db.Queries, userID int64, entryType string, deltaAvail, deltaFrozen int64, refType string, refID int64) error {
	n, err := q.InsertLedgerEntry(ctx, db.InsertLedgerEntryParams{
		UserID:         userID,
		EntryType:      entryType,
		DeltaAvailable: deltaAvail,
		DeltaFrozen:    deltaFrozen,
		RefType:        refType,
		RefID:          refID,
	})
	if err != nil {
		return fmt.Errorf("insert %s ledger entry: %w", entryType, err)
	}
	if n == 0 {
		return ErrDuplicateEntry
	}
	return nil
}
