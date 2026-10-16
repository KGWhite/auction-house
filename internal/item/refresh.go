package item

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"auctionhouse/internal/auction"
	"auctionhouse/internal/config"
	"auctionhouse/internal/db"
)

// Rand is the subset of math/rand/v2 used by the generator; tests inject a
// seeded source.
type Rand interface {
	IntN(n int) int
	Int64N(n int64) int64
	Shuffle(n int, swap func(i, j int))
}

type globalRand struct{}

func (globalRand) IntN(n int) int                     { return rand.IntN(n) }
func (globalRand) Int64N(n int64) int64               { return rand.Int64N(n) }
func (globalRand) Shuffle(n int, swap func(i, j int)) { rand.Shuffle(n, swap) }

// GlobalRand is the default, concurrency-safe random source.
var GlobalRand Rand = globalRand{}

// RefreshResult summarises one generated round.
type RefreshResult struct {
	RoundID  int64
	Auctions int
}

// Refresh creates one auction round in a single transaction: for every level
// bracket in [BracketMin, BracketMax] it draws ItemsPerBracket templates whose
// min_level fits the bracket, rolls an item level inside the bracket, prices
// the item and gives the auction a random duration in
// [AuctionMinDuration, AuctionMaxDuration]. End times are computed by the
// database (now() + duration).
func Refresh(ctx context.Context, pool *pgxpool.Pool, cfg config.Config, rng Rand) (RefreshResult, error) {
	if rng == nil {
		rng = GlobalRand
	}
	var res RefreshResult
	err := db.InTx(ctx, pool, func(q *db.Queries) error {
		var err error
		res, err = createRound(ctx, q, cfg, rng)
		return err
	})
	return res, err
}

// RefreshIfDue creates a round only when the newest round is at least
// REFRESH_INTERVAL old on the database clock. A transaction-scoped advisory
// lock serialises concurrent workers, so even with several workers (or after
// a restart) a round is created once per interval. The bool reports whether a
// round was created.
func RefreshIfDue(ctx context.Context, pool *pgxpool.Pool, cfg config.Config, rng Rand) (RefreshResult, bool, error) {
	if rng == nil {
		rng = GlobalRand
	}
	var res RefreshResult
	created := false
	err := db.InTx(ctx, pool, func(q *db.Queries) error {
		if err := q.LockRefresh(ctx); err != nil {
			return fmt.Errorf("lock refresh: %w", err)
		}
		due, err := q.RefreshDue(ctx, cfg.RefreshInterval.Milliseconds())
		if err != nil {
			return fmt.Errorf("check refresh due: %w", err)
		}
		if !due {
			return nil
		}
		// Nothing to sell before the item templates are seeded; do not burn the
		// interval on an empty round.
		if n, err := q.CountItemTemplates(ctx); err != nil {
			return fmt.Errorf("count templates: %w", err)
		} else if n == 0 {
			return nil
		}
		res, err = createRound(ctx, q, cfg, rng)
		created = err == nil
		return err
	})
	return res, created, err
}

func createRound(ctx context.Context, q *db.Queries, cfg config.Config, rng Rand) (RefreshResult, error) {
	round, err := q.CreateAuctionRound(ctx)
	if err != nil {
		return RefreshResult{}, fmt.Errorf("create round: %w", err)
	}
	res := RefreshResult{RoundID: round.ID}
	for bracket := cfg.BracketMin; bracket <= cfg.BracketMax; bracket++ {
		n, err := createBracketAuctions(ctx, q, cfg, rng, round.ID, bracket)
		if err != nil {
			return res, fmt.Errorf("bracket %d: %w", bracket, err)
		}
		res.Auctions += n
	}
	return res, nil
}

func createBracketAuctions(ctx context.Context, q *db.Queries, cfg config.Config, rng Rand, roundID int64, bracket int) (int, error) {
	lo, hi := auction.BracketLevelRange(bracket)
	if lo < 1 {
		lo = 1 // level 0 items do not exist
	}
	tmpls, err := q.ListTemplatesUpToLevel(ctx, int32(hi))
	if err != nil {
		return 0, err
	}
	if len(tmpls) == 0 {
		return 0, nil
	}
	rng.Shuffle(len(tmpls), func(i, j int) { tmpls[i], tmpls[j] = tmpls[j], tmpls[i] })

	spanMs := (cfg.AuctionMaxDuration - cfg.AuctionMinDuration).Milliseconds()
	created := 0
	for i := 0; i < cfg.ItemsPerBracket; i++ {
		// Draw without replacement; reuse templates only when there are fewer
		// eligible templates than items to create.
		t := tmpls[i%len(tmpls)]
		levelLo := max(lo, int(t.MinLevel))
		level := levelLo + rng.IntN(hi-levelLo+1)

		it, err := q.CreateItem(ctx, db.CreateItemParams{TemplateID: t.ID, Level: int32(level)})
		if err != nil {
			return created, err
		}
		duration := cfg.AuctionMinDuration + time.Duration(rng.Int64N(spanMs+1))*time.Millisecond
		if _, err := q.CreateAuction(ctx, db.CreateAuctionParams{
			RoundID:      roundID,
			ItemID:       it.ID,
			LevelBracket: int32(bracket),
			StartPrice:   auction.StartPrice(t.BaseValue, level),
			DurationMs:   duration.Milliseconds(),
		}); err != nil {
			return created, err
		}
		created++
	}
	return created, nil
}
