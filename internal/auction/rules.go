// Package auction implements the auction rules, bidding and settlement.
package auction

import (
	"time"

	"auctionhouse/internal/config"
)

// BracketSize is the number of player levels per level bracket.
const BracketSize = 10

// Bracket returns the level bracket of a player or item level: floor(level / 10).
func Bracket(level int) int {
	if level < 0 {
		return 0
	}
	return level / BracketSize
}

// BracketLevelRange returns the inclusive level range [lo, hi] of a bracket.
func BracketLevelRange(bracket int) (lo, hi int) {
	return bracket * BracketSize, bracket*BracketSize + BracketSize - 1
}

// StartPrice is base_value * (1 + level/20), truncated to an integer. The
// computation stays in integer arithmetic: base * (20 + level) / 20.
func StartPrice(baseValue int64, itemLevel int) int64 {
	return baseValue * int64(20+itemLevel) / 20
}

// Rules holds the bidding and display thresholds taken from the configuration.
type Rules struct {
	IncrementPercent int64
	IncrementMin     int64
	Bands            Bands
}

// RulesFromConfig extracts the auction rules from the configuration.
func RulesFromConfig(c config.Config) Rules {
	return Rules{
		IncrementPercent: c.BidIncrementPercent,
		IncrementMin:     c.BidIncrementMin,
		Bands: Bands{
			VeryShort: c.BandVeryShort,
			Short:     c.BandShort,
			Medium:    c.BandMedium,
			Long:      c.BandLong,
		},
	}
}

// MinNextBid returns the lowest acceptable next bid. Without bids it is the
// start price; otherwise the current price plus max(ceil(current * percent /
// 100), min increment).
func (r Rules) MinNextBid(hasBids bool, startPrice, currentPrice int64) int64 {
	if !hasBids {
		return startPrice
	}
	inc := (currentPrice*r.IncrementPercent + 99) / 100 // integer ceil
	if inc < r.IncrementMin {
		inc = r.IncrementMin
	}
	return currentPrice + inc
}

// Band is a coarse remaining-time label; the exact end time is never exposed.
type Band string

// Time bands, from least to most remaining time.
const (
	BandVeryShort Band = "very_short"
	BandShort     Band = "short"
	BandMedium    Band = "medium"
	BandLong      Band = "long"
	BandVeryLong  Band = "very_long"
)

// Bands holds the upper bounds (exclusive) of each band.
type Bands struct {
	VeryShort time.Duration
	Short     time.Duration
	Medium    time.Duration
	Long      time.Duration
}

// Of converts a remaining duration (computed in SQL as end_at - now()) to a band.
func (b Bands) Of(remaining time.Duration) Band {
	switch {
	case remaining < b.VeryShort:
		return BandVeryShort
	case remaining < b.Short:
		return BandShort
	case remaining < b.Medium:
		return BandMedium
	case remaining < b.Long:
		return BandLong
	default:
		return BandVeryLong
	}
}
