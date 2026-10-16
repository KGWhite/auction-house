package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"

	"auctionhouse/internal/auction"
	"auctionhouse/internal/item"
)

// auctionJSON is the public representation of an auction. It has no end time
// on purpose; only the time band is exposed.
type auctionJSON struct {
	ID            int64    `json:"id"`
	Item          itemJSON `json:"item"`
	LevelBracket  int      `json:"level_bracket"`
	CurrentPrice  int64    `json:"current_price"`
	MinNextBid    int64    `json:"min_next_bid"`
	TopBidderName *string  `json:"top_bidder_name"`
	IsMine        bool     `json:"is_mine"`
	IHaveBid      bool     `json:"i_have_bid"`
	HasBids       bool     `json:"has_bids"`
	TimeBand      string   `json:"time_band"`
	Status        string   `json:"status"`
}

type itemJSON struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Level int    `json:"level"`
}

func toAuctionJSON(v auction.View) auctionJSON {
	return auctionJSON{
		ID:            v.ID,
		Item:          itemJSON{Name: v.Item.Name, Type: v.Item.Type, Level: v.Item.Level},
		LevelBracket:  v.LevelBracket,
		CurrentPrice:  v.CurrentPrice,
		MinNextBid:    v.MinNextBid,
		TopBidderName: v.TopBidderName,
		IsMine:        v.IsMine,
		IHaveBid:      v.IHaveBid,
		HasBids:       v.HasBids,
		TimeBand:      string(v.TimeBand),
		Status:        v.Status,
	}
}

func (s *Server) listAuctions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := auction.ListFilter{Bracket: -1, ItemType: q.Get("type"), Sort: q.Get("sort")}
	if v := q.Get("bracket"); v != "" {
		b, err := strconv.Atoi(v)
		if err != nil || b < 0 {
			writeError(w, http.StatusBadRequest, "invalid_request", "bracket must be a non-negative integer")
			return
		}
		f.Bracket = b
	}
	if f.ItemType != "" && !slices.Contains(item.Types, f.ItemType) {
		writeError(w, http.StatusBadRequest, "invalid_request", "unknown item type")
		return
	}
	if !auction.ValidSort(f.Sort) {
		writeError(w, http.StatusBadRequest, "invalid_request", "unknown sort order")
		return
	}
	views, err := s.auction.ListViews(r.Context(), s.viewerID(r), f)
	if err != nil {
		s.internalError(w, err)
		return
	}
	out := make([]auctionJSON, 0, len(views))
	for _, v := range views {
		out = append(out, toAuctionJSON(v))
	}
	writeJSON(w, http.StatusOK, map[string]any{"auctions": out})
}

func (s *Server) getAuction(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	v, err := s.auction.GetView(r.Context(), s.viewerID(r), id)
	if err != nil {
		s.writeAuctionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toAuctionJSON(v))
}

type bidRequest struct {
	Amount *int64 `json:"amount"`
}

type bidResponse struct {
	BidID        int64 `json:"bid_id"`
	AuctionID    int64 `json:"auction_id"`
	Amount       int64 `json:"amount"`
	CurrentPrice int64 `json:"current_price"`
	MinNextBid   int64 `json:"min_next_bid"`
	Replayed     bool  `json:"replayed"`
}

const maxIdempotencyKeyLen = 200

func (s *Server) placeBid(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	auctionID, ok := pathID(w, r)
	if !ok {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "missing_idempotency_key", "the Idempotency-Key header is required")
		return
	}
	if len(key) > maxIdempotencyKeyLen {
		writeError(w, http.StatusBadRequest, "invalid_request", "Idempotency-Key is too long")
		return
	}
	var req bidRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10))
	if err := dec.Decode(&req); err != nil || req.Amount == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", `body must be {"amount": <integer>}`)
		return
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_request", "unexpected data after the JSON body")
		return
	}

	res, err := s.auction.PlaceBid(r.Context(), uid, auctionID, *req.Amount, key)
	if err != nil {
		s.writeAuctionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, bidResponse{
		BidID:        res.BidID,
		AuctionID:    res.AuctionID,
		Amount:       res.Amount,
		CurrentPrice: res.CurrentPrice,
		MinNextBid:   res.MinNextBid,
		Replayed:     res.Replayed,
	})
}

// writeAuctionError maps domain errors to HTTP statuses and the uniform error body.
func (s *Server) writeAuctionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auction.ErrBidTooLow):
		min, _ := auction.MinNextBidFromError(err)
		writeJSON(w, http.StatusConflict, errorBody{Error: errorDetail{
			Code: "bid_too_low", Message: "the bid is below the minimum next bid", MinNextBid: &min,
		}})
	case errors.Is(err, auction.ErrAlreadyTopBidder):
		writeError(w, http.StatusConflict, "already_top_bidder", "you are already the top bidder")
	case errors.Is(err, auction.ErrAuctionEnded):
		writeError(w, http.StatusGone, "auction_ended", "the auction has ended")
	case errors.Is(err, auction.ErrInsufficientGold):
		writeError(w, http.StatusUnprocessableEntity, "insufficient_gold", "not enough available gold")
	case errors.Is(err, auction.ErrLevelBracketMismatch):
		writeError(w, http.StatusUnprocessableEntity, "level_bracket_mismatch", "this auction is outside your level bracket")
	case errors.Is(err, auction.ErrAuctionNotFound):
		writeError(w, http.StatusNotFound, "auction_not_found", "auction not found")
	case errors.Is(err, auction.ErrUserNotFound):
		writeError(w, http.StatusUnauthorized, "unauthorized", "unknown player")
	case errors.Is(err, auction.ErrMissingIdempotencyKey):
		writeError(w, http.StatusBadRequest, "missing_idempotency_key", "the Idempotency-Key header is required")
	default:
		s.internalError(w, err)
	}
}

func (s *Server) internalError(w http.ResponseWriter, err error) {
	s.logger.Printf("internal error: %v", err)
	writeError(w, http.StatusInternalServerError, "internal_error", "internal error")
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, "auction_not_found", "auction not found")
		return 0, false
	}
	return id, true
}
