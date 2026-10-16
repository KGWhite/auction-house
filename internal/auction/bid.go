package auction

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"auctionhouse/internal/db"
	"auctionhouse/internal/wallet"
)

// Service runs the auction use cases against PostgreSQL.
type Service struct {
	pool  *pgxpool.Pool
	rules Rules
}

// NewService creates a Service.
func NewService(pool *pgxpool.Pool, rules Rules) *Service {
	return &Service{pool: pool, rules: rules}
}

// Rules returns the rules the service applies.
func (s *Service) Rules() Rules { return s.rules }

// BidResult describes an accepted bid.
type BidResult struct {
	BidID        int64
	AuctionID    int64
	UserID       int64
	Amount       int64
	CurrentPrice int64 // the auction's current price (equals Amount unless replayed after a later bid)
	MinNextBid   int64 // the minimum amount for the next bid on this auction
	Replayed     bool  // true when the idempotency key matched an earlier bid
}

// PlaceBid places a bid for userID on auctionID inside a single transaction.
//
// The auction row is locked first (SELECT ... FOR UPDATE), which serialises all
// bids on one auction; user rows are then locked in ascending id order; the
// bid, the freeze, the refund of the previous top bidder and the price update
// commit together or not at all.
func (s *Service) PlaceBid(ctx context.Context, userID, auctionID, amount int64, idempotencyKey string) (BidResult, error) {
	if idempotencyKey == "" {
		return BidResult{}, ErrMissingIdempotencyKey
	}
	var res BidResult
	err := db.InTx(ctx, s.pool, func(q *db.Queries) error {
		var err error
		res, err = s.placeBid(ctx, q, userID, auctionID, amount, idempotencyKey)
		return err
	})
	if err != nil {
		return BidResult{}, err
	}
	return res, nil
}

func (s *Service) placeBid(ctx context.Context, q *db.Queries, userID, auctionID, amount int64, key string) (BidResult, error) {
	// 1. Idempotency: a repeated (user, key) returns the original result.
	if r, found, err := s.replay(ctx, q, userID, key); err != nil || found {
		return r, err
	}

	// 2. Lock the auction row and, in the same query, decide with the database
	//    clock whether it is still open.
	a, err := q.LockAuctionForBid(ctx, auctionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return BidResult{}, ErrAuctionNotFound
	}
	if err != nil {
		return BidResult{}, fmt.Errorf("lock auction: %w", err)
	}

	// Concurrent retries of the same request queue up on the auction lock; the
	// winner has committed by now, so look again before validating (otherwise
	// the retry would be rejected as "already top bidder" instead of replayed).
	if r, found, err := s.replay(ctx, q, userID, key); err != nil || found {
		return r, err
	}

	// 3. Validate.
	if a.Status != "active" || !a.IsOpen {
		return BidResult{}, ErrAuctionEnded
	}
	if a.TopBidderID != nil && *a.TopBidderID == userID {
		return BidResult{}, ErrAlreadyTopBidder
	}
	user, err := q.GetUser(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return BidResult{}, ErrUserNotFound
	}
	if err != nil {
		return BidResult{}, fmt.Errorf("load user: %w", err)
	}
	if Bracket(int(user.Level)) != int(a.LevelBracket) {
		return BidResult{}, ErrLevelBracketMismatch
	}
	hasBids := a.TopBidderID != nil
	minNext := s.rules.MinNextBid(hasBids, a.StartPrice, a.CurrentPrice)
	if amount < minNext {
		return BidResult{}, &BidTooLowError{MinNextBid: minNext}
	}

	// The latest bid of a locked auction is the top bid; we need its id and
	// amount to refund the previous top bidder.
	var prev *db.Bid
	if hasBids {
		b, err := q.GetLatestBid(ctx, auctionID)
		if err != nil {
			return BidResult{}, fmt.Errorf("load top bid: %w", err)
		}
		if b.UserID != *a.TopBidderID || b.Amount != a.CurrentPrice {
			return BidResult{}, fmt.Errorf("auction %d: latest bid (user %d, %d) disagrees with top bidder %d at %d",
				auctionID, b.UserID, b.Amount, *a.TopBidderID, a.CurrentPrice)
		}
		prev = &b
	}

	// 4. Lock the new bidder and the previous top bidder, smallest id first.
	//    Always taking user locks in ascending id order prevents deadlocks when
	//    two players outbid each other on different auctions at the same time
	//    (A->B on one auction, B->A on another would otherwise lock the same two
	//    rows in opposite orders). See wallet.LockUsers.
	lockIDs := []int64{userID}
	if prev != nil {
		lockIDs = append(lockIDs, prev.UserID)
	}
	if err := wallet.LockUsers(ctx, q, lockIDs...); err != nil {
		if errors.Is(err, wallet.ErrUserNotFound) {
			return BidResult{}, ErrUserNotFound
		}
		return BidResult{}, err
	}

	// 5. Record the bid. The timestamp is the database clock reading taken when
	//    the open/closed decision was made.
	bid, err := q.InsertBid(ctx, db.InsertBidParams{
		AuctionID:      auctionID,
		UserID:         userID,
		Amount:         amount,
		IdempotencyKey: key,
		CreatedAt:      a.CheckedAt,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// Lost a race on (user_id, idempotency_key) that the checks above could
		// not see (the same key used on another auction): replay the original.
		r, found, rerr := s.replay(ctx, q, userID, key)
		if rerr != nil {
			return BidResult{}, rerr
		}
		if !found {
			return BidResult{}, fmt.Errorf("idempotency conflict without a stored bid")
		}
		return r, nil
	}
	if err != nil {
		return BidResult{}, fmt.Errorf("insert bid: %w", err)
	}

	// 6. Freeze the new bidder's gold (conditional update + ledger entry).
	if err := wallet.Freeze(ctx, q, userID, amount, bid.ID); err != nil {
		return BidResult{}, err
	}

	// 7. Refund the previous top bidder.
	if prev != nil {
		if err := wallet.Unfreeze(ctx, q, prev.UserID, prev.Amount, prev.ID); err != nil {
			return BidResult{}, err
		}
	}

	// 8. Publish the new price and leader.
	if err := q.UpdateAuctionTopBid(ctx, db.UpdateAuctionTopBidParams{ID: auctionID, CurrentPrice: amount, TopBidderID: &userID}); err != nil {
		return BidResult{}, fmt.Errorf("update auction: %w", err)
	}

	return BidResult{
		BidID:        bid.ID,
		AuctionID:    auctionID,
		UserID:       userID,
		Amount:       amount,
		CurrentPrice: amount,
		MinNextBid:   s.rules.MinNextBid(true, a.StartPrice, amount),
	}, nil
}

// replay returns the stored result of an earlier bid with the same
// (user, idempotency key), if any.
func (s *Service) replay(ctx context.Context, q *db.Queries, userID int64, key string) (BidResult, bool, error) {
	bid, err := q.GetBidByIdempotencyKey(ctx, db.GetBidByIdempotencyKeyParams{UserID: userID, IdempotencyKey: key})
	if errors.Is(err, pgx.ErrNoRows) {
		return BidResult{}, false, nil
	}
	if err != nil {
		return BidResult{}, false, fmt.Errorf("lookup idempotency key: %w", err)
	}
	p, err := q.GetAuctionPricing(ctx, bid.AuctionID)
	if err != nil {
		return BidResult{}, false, fmt.Errorf("load auction pricing: %w", err)
	}
	return BidResult{
		BidID:        bid.ID,
		AuctionID:    bid.AuctionID,
		UserID:       userID,
		Amount:       bid.Amount,
		CurrentPrice: p.CurrentPrice,
		MinNextBid:   s.rules.MinNextBid(p.TopBidderID != nil, p.StartPrice, p.CurrentPrice),
		Replayed:     true,
	}, true, nil
}
