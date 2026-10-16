package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"auctionhouse/internal/auction"
	"auctionhouse/internal/db"
	"auctionhouse/internal/item"
)

type meJSON struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	Level         int    `json:"level"`
	LevelBracket  int    `json:"level_bracket"`
	GoldAvailable int64  `json:"gold_available"`
	GoldFrozen    int64  `json:"gold_frozen"`
	InboxCount    int64  `json:"inbox_count"`
}

func (s *Server) getMe(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	u, err := s.queries.GetUser(r.Context(), uid)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "unknown player")
		return
	}
	if err != nil {
		s.internalError(w, err)
		return
	}
	n, err := s.queries.CountInbox(r.Context(), uid)
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, meJSON{
		ID: u.ID, Name: u.Name, Level: int(u.Level), LevelBracket: auction.Bracket(int(u.Level)),
		GoldAvailable: u.GoldAvailable, GoldFrozen: u.GoldFrozen, InboxCount: n,
	})
}

type inboxJSON struct {
	ID        int64     `json:"id"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
	AuctionID *int64    `json:"auction_id"`
	Item      *struct {
		ID    int64  `json:"id"`
		Name  string `json:"name"`
		Type  string `json:"type"`
		Level int    `json:"level"`
	} `json:"item"`
	PricePaid *int64 `json:"price_paid"`
}

func (s *Server) getInbox(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	rows, err := s.queries.ListInbox(r.Context(), uid)
	if err != nil {
		s.internalError(w, err)
		return
	}
	out := make([]inboxJSON, 0, len(rows))
	for _, row := range rows {
		e := inboxJSON{ID: row.ID, Message: row.Message, CreatedAt: row.CreatedAt, AuctionID: row.AuctionID, PricePaid: row.PricePaid}
		if row.ItemID != nil && row.ItemName != nil && row.ItemType != nil && row.ItemLevel != nil {
			e.Item = &struct {
				ID    int64  `json:"id"`
				Name  string `json:"name"`
				Type  string `json:"type"`
				Level int    `json:"level"`
			}{ID: *row.ItemID, Name: *row.ItemName, Type: *row.ItemType, Level: int(*row.ItemLevel)}
		}
		out = append(out, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

type ledgerEntryJSON struct {
	ID             int64     `json:"id"`
	EntryType      string    `json:"entry_type"`
	DeltaAvailable int64     `json:"delta_available"`
	DeltaFrozen    int64     `json:"delta_frozen"`
	RefType        string    `json:"ref_type"`
	RefID          int64     `json:"ref_id"`
	CreatedAt      time.Time `json:"created_at"`
}

const (
	defaultLedgerLimit = 50
	maxLedgerLimit     = 200
)

// getLedger lists the player's ledger newest first. Pagination is keyset-based:
// pass the previous response's next_before_id as before_id for the next page.
func (s *Server) getLedger(w http.ResponseWriter, r *http.Request) {
	uid, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	limit, beforeID := defaultLedgerLimit, int64(0)
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxLedgerLimit {
			writeError(w, http.StatusBadRequest, "invalid_request", "limit must be between 1 and 200")
			return
		}
		limit = n
	}
	if v := r.URL.Query().Get("before_id"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "invalid_request", "before_id must be a non-negative integer")
			return
		}
		beforeID = n
	}
	rows, err := s.queries.ListLedger(r.Context(), db.ListLedgerParams{UserID: uid, BeforeID: beforeID, RowLimit: int32(limit + 1)})
	if err != nil {
		s.internalError(w, err)
		return
	}
	var next *int64
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1].ID
		next = &last
	}
	entries := make([]ledgerEntryJSON, 0, len(rows))
	for _, e := range rows {
		entries = append(entries, ledgerEntryJSON{
			ID: e.ID, EntryType: e.EntryType, DeltaAvailable: e.DeltaAvailable, DeltaFrozen: e.DeltaFrozen,
			RefType: e.RefType, RefID: e.RefID, CreatedAt: e.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries, "next_before_id": next})
}

// --- development-only endpoints ---

type userJSON struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Level        int    `json:"level"`
	LevelBracket int    `json:"level_bracket"`
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.queries.ListUsers(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	out := make([]userJSON, 0, len(users))
	for _, u := range users {
		out = append(out, userJSON{ID: u.ID, Name: u.Name, Level: int(u.Level), LevelBracket: auction.Bracket(int(u.Level))})
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out})
}

func (s *Server) devRefresh(w http.ResponseWriter, r *http.Request) {
	res, err := item.Refresh(r.Context(), s.pool, s.cfg, nil)
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"round_id": res.RoundID, "auctions": res.Auctions})
}
