package auction

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"auctionhouse/internal/db"
	"auctionhouse/internal/wallet"
)

// SettleStats counts what one SettleDue call did.
type SettleStats struct {
	Settled int // auctions with a winner: gold spent, item delivered
	Expired int // auctions that ended without bids
	Skipped int // auctions another worker (or an in-flight bid) held, or that were already done
	Found   int // due auctions returned by the batch query
}

// SettleDue settles one batch of ended auctions (at most batchSize).
//
// Step 1 lists the due auction ids (FOR UPDATE SKIP LOCKED, oldest first).
// Step 2 settles every auction in its own transaction, which locks the row
// again, re-checks that it is still active and due, and either completes the
// settlement or does nothing. That makes the whole operation idempotent and
// safe to run from several workers at once: a second worker skips rows the
// first one holds and ignores rows it has already finished, and the unique
// ledger key on ('spend', 'auction', id) is a last line of defence against a
// double charge.
//
// A failure on one auction does not stop the batch; the errors are joined.
func (s *Service) SettleDue(ctx context.Context, batchSize int) (SettleStats, error) {
	var stats SettleStats
	var ids []int64
	err := db.InTx(ctx, s.pool, func(q *db.Queries) error {
		var err error
		ids, err = q.ListDueAuctionIDs(ctx, int32(batchSize))
		return err
	})
	if err != nil {
		return stats, fmt.Errorf("list due auctions: %w", err)
	}
	stats.Found = len(ids)

	var errs []error
	for _, id := range ids {
		outcome, err := s.settleOne(ctx, id)
		if err != nil {
			errs = append(errs, fmt.Errorf("settle auction %d: %w", id, err))
			continue
		}
		switch outcome {
		case outcomeSettled:
			stats.Settled++
		case outcomeExpired:
			stats.Expired++
		default:
			stats.Skipped++
		}
	}
	return stats, errors.Join(errs...)
}

type settleOutcome int

const (
	outcomeSkipped settleOutcome = iota
	outcomeSettled
	outcomeExpired
)

func (s *Service) settleOne(ctx context.Context, auctionID int64) (settleOutcome, error) {
	outcome := outcomeSkipped
	err := db.InTx(ctx, s.pool, func(q *db.Queries) error {
		a, err := q.LockDueAuctionForSettle(ctx, auctionID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // locked by someone else, already settled, or not due
		}
		if err != nil {
			return fmt.Errorf("lock auction: %w", err)
		}

		if a.TopBidderID == nil {
			if err := setStatus(ctx, q, auctionID, "expired"); err != nil {
				return err
			}
			outcome = outcomeExpired
			return nil
		}

		winner := *a.TopBidderID
		if err := wallet.LockUsers(ctx, q, winner); err != nil {
			return err
		}
		if err := wallet.Spend(ctx, q, winner, a.CurrentPrice, auctionID); err != nil {
			return err
		}
		if n, err := q.SetItemOwner(ctx, db.SetItemOwnerParams{ID: a.ItemID, OwnerID: &winner}); err != nil {
			return fmt.Errorf("set item owner: %w", err)
		} else if n != 1 {
			return fmt.Errorf("item %d not found", a.ItemID)
		}
		if _, err := q.InsertInbox(ctx, db.InsertInboxParams{
			UserID:    winner,
			ItemID:    &a.ItemID,
			AuctionID: &auctionID,
			Message:   fmt.Sprintf("You won auction #%d for %d gold.", auctionID, a.CurrentPrice),
		}); err != nil {
			return fmt.Errorf("insert inbox: %w", err)
		}
		if err := setStatus(ctx, q, auctionID, "settled"); err != nil {
			return err
		}
		outcome = outcomeSettled
		return nil
	})
	if err != nil {
		return outcomeSkipped, err
	}
	return outcome, nil
}

func setStatus(ctx context.Context, q *db.Queries, auctionID int64, status string) error {
	n, err := q.SetAuctionStatus(ctx, db.SetAuctionStatusParams{ID: auctionID, Status: status})
	if err != nil {
		return fmt.Errorf("set status %s: %w", status, err)
	}
	if n != 1 {
		return fmt.Errorf("auction %d is no longer active", auctionID)
	}
	return nil
}
