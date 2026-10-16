package auction

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"auctionhouse/internal/db"
)

// Sort orders accepted by ListViews.
const (
	SortEnding    = "ending" // default: soonest end first (reveals order only, never the time)
	SortPriceAsc  = "price_asc"
	SortPriceDesc = "price_desc"
	SortLevelDesc = "level_desc"
)

// ValidSort reports whether s is a supported sort order ("" means the default).
func ValidSort(s string) bool {
	switch s {
	case "", SortEnding, SortPriceAsc, SortPriceDesc, SortLevelDesc:
		return true
	}
	return false
}

// ItemInfo describes the item on offer.
type ItemInfo struct {
	Name  string
	Type  string
	Level int
}

// View is an auction as shown to one player. It deliberately has no end time:
// only the coarse time band is exposed.
type View struct {
	ID            int64
	Item          ItemInfo
	LevelBracket  int
	CurrentPrice  int64
	MinNextBid    int64
	TopBidderName *string
	IsMine        bool // the viewer is the top bidder
	IHaveBid      bool // the viewer has bid on this auction at some point
	HasBids       bool
	TimeBand      Band
	Status        string
}

// ListFilter selects auctions for ListViews. Bracket < 0 and an empty
// ItemType mean "no filter".
type ListFilter struct {
	Bracket  int
	ItemType string
	Sort     string
}

// GetView returns one auction as seen by viewerID (0 for an anonymous viewer).
func (s *Service) GetView(ctx context.Context, viewerID, auctionID int64) (View, error) {
	row, err := db.New(s.pool).GetAuctionView(ctx, db.GetAuctionViewParams{ViewerID: viewerID, AuctionID: auctionID})
	if errors.Is(err, pgx.ErrNoRows) {
		return View{}, ErrAuctionNotFound
	}
	if err != nil {
		return View{}, fmt.Errorf("get auction view: %w", err)
	}
	return s.newView(row), nil
}

// ListViews returns the active auctions matching f as seen by viewerID.
func (s *Service) ListViews(ctx context.Context, viewerID int64, f ListFilter) ([]View, error) {
	rows, err := db.New(s.pool).ListAuctionViews(ctx, db.ListAuctionViewsParams{
		ViewerID: viewerID,
		Bracket:  int32(f.Bracket),
		ItemType: f.ItemType,
		Sort:     f.Sort,
	})
	if err != nil {
		return nil, fmt.Errorf("list auction views: %w", err)
	}
	out := make([]View, 0, len(rows))
	for _, r := range rows {
		out = append(out, s.newView(db.GetAuctionViewRow(r)))
	}
	return out, nil
}

func (s *Service) newView(r db.GetAuctionViewRow) View {
	hasBids := r.TopBidderID != nil
	return View{
		ID:            r.ID,
		Item:          ItemInfo{Name: r.ItemName, Type: r.ItemType, Level: int(r.ItemLevel)},
		LevelBracket:  int(r.LevelBracket),
		CurrentPrice:  r.CurrentPrice,
		MinNextBid:    s.rules.MinNextBid(hasBids, r.StartPrice, r.CurrentPrice),
		TopBidderName: r.TopBidderName,
		IsMine:        r.IsMine,
		IHaveBid:      r.IHaveBid,
		HasBids:       hasBids,
		TimeBand:      s.rules.Bands.Of(time.Duration(r.RemainingMs) * time.Millisecond),
		Status:        r.Status,
	}
}
