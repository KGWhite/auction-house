// Package httpapi exposes the auction house over HTTP/JSON.
package httpapi

import (
	"encoding/json"
	"log"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"auctionhouse/internal/auction"
	"auctionhouse/internal/config"
	"auctionhouse/internal/db"
)

// DevUserHeader identifies the acting player in development mode.
const DevUserHeader = "X-Dev-User-Id"

// Server wires the HTTP handlers to the domain services.
type Server struct {
	cfg     config.Config
	pool    *pgxpool.Pool
	auction *auction.Service
	queries *db.Queries
	logger  *log.Logger
}

// New builds the HTTP handler. Development-only routes (/api/users and
// /api/dev/*) are registered only when cfg.AppEnv is "dev"; outside dev there
// is no way to identify a player yet, so /api/me and bidding answer 401.
func New(cfg config.Config, pool *pgxpool.Pool, svc *auction.Service, logger *log.Logger) http.Handler {
	if logger == nil {
		logger = log.Default()
	}
	s := &Server{cfg: cfg, pool: pool, auction: svc, queries: db.New(pool), logger: logger}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/auctions", s.listAuctions)
	mux.HandleFunc("GET /api/auctions/{id}", s.getAuction)
	mux.HandleFunc("POST /api/auctions/{id}/bids", s.placeBid)
	mux.HandleFunc("GET /api/me", s.getMe)
	mux.HandleFunc("GET /api/me/inbox", s.getInbox)
	mux.HandleFunc("GET /api/me/ledger", s.getLedger)
	if cfg.IsDev() {
		mux.HandleFunc("GET /api/users", s.listUsers)
		mux.HandleFunc("POST /api/dev/refresh", s.devRefresh)
	}
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "no such endpoint")
	})
	return s.middleware(mux)
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			if v := recover(); v != nil {
				s.logger.Printf("panic: %v\n%s", v, debug.Stack())
				if !rec.wrote {
					writeError(rec, http.StatusInternalServerError, "internal_error", "internal error")
				}
			}
			s.logger.Printf("%s %s -> %d (%s)", r.Method, r.URL.Path, rec.status, time.Since(start).Round(time.Microsecond))
		}()
		next.ServeHTTP(rec, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status, r.wrote = code, true
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	r.wrote = true
	return r.ResponseWriter.Write(b)
}

// viewerID returns the player id from the dev header, or 0 when absent,
// malformed, or when development mode is off.
func (s *Server) viewerID(r *http.Request) int64 {
	if !s.cfg.IsDev() {
		return 0
	}
	v := strings.TrimSpace(r.Header.Get(DevUserHeader))
	if v == "" {
		return 0
	}
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil || id <= 0 {
		return 0
	}
	return id
}

// requireUser returns the acting player id or writes a 401 and returns false.
func (s *Server) requireUser(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id := s.viewerID(r)
	if id == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "a valid "+DevUserHeader+" header is required")
		return 0, false
	}
	return id, true
}

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	MinNextBid *int64 `json:"min_next_bid,omitempty"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorBody{Error: errorDetail{Code: code, Message: message}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
