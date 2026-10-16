package auction

import (
	"errors"
	"fmt"

	"auctionhouse/internal/wallet"
)

// Sentinel errors returned by PlaceBid. The HTTP layer maps them to status codes.
var (
	ErrAuctionNotFound       = errors.New("auction not found")
	ErrAuctionEnded          = errors.New("auction has ended")
	ErrBidTooLow             = errors.New("bid is below the minimum next bid")
	ErrAlreadyTopBidder      = errors.New("you are already the top bidder")
	ErrInsufficientGold      = wallet.ErrInsufficientGold
	ErrLevelBracketMismatch  = errors.New("auction is outside your level bracket")
	ErrUserNotFound          = errors.New("user not found")
	ErrMissingIdempotencyKey = errors.New("idempotency key is required")
)

// BidTooLowError is the concrete error behind ErrBidTooLow; it carries the
// minimum amount that would have been accepted.
type BidTooLowError struct {
	MinNextBid int64
}

func (e *BidTooLowError) Error() string {
	return fmt.Sprintf("%v (minimum next bid is %d)", ErrBidTooLow, e.MinNextBid)
}

// Is makes errors.Is(err, ErrBidTooLow) true.
func (e *BidTooLowError) Is(target error) bool { return target == ErrBidTooLow }

// MinNextBidFromError extracts the minimum next bid from an ErrBidTooLow error.
func MinNextBidFromError(err error) (int64, bool) {
	var e *BidTooLowError
	if errors.As(err, &e) {
		return e.MinNextBid, true
	}
	return 0, false
}
