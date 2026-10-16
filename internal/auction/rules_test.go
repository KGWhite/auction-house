package auction

import (
	"testing"
	"time"

	"auctionhouse/internal/config"
)

var defaultRules = RulesFromConfig(config.Default())

func TestBracket(t *testing.T) {
	cases := map[int]int{0: 0, 1: 0, 5: 0, 9: 0, 10: 1, 14: 1, 19: 1, 20: 2, 23: 2, 24: 2, 37: 3, 48: 4, 59: 5, 60: 6}
	for level, want := range cases {
		if got := Bracket(level); got != want {
			t.Errorf("Bracket(%d) = %d, want %d", level, got, want)
		}
	}
}

func TestBracketLevelRange(t *testing.T) {
	lo, hi := BracketLevelRange(3)
	if lo != 30 || hi != 39 {
		t.Fatalf("got [%d,%d], want [30,39]", lo, hi)
	}
}

func TestStartPrice(t *testing.T) {
	cases := []struct {
		base  int64
		level int
		want  int64
	}{
		{100, 0, 100},
		{100, 20, 200},
		{100, 10, 150},
		{100, 5, 125},
		{1000, 1, 1050},
		{250, 7, 337}, // 250 * 1.35 = 337.5 -> truncated
		{1500, 45, 4875},
		{99, 3, 113}, // 99 * 1.15 = 113.85 -> truncated
	}
	for _, c := range cases {
		if got := StartPrice(c.base, c.level); got != c.want {
			t.Errorf("StartPrice(%d, %d) = %d, want %d", c.base, c.level, got, c.want)
		}
	}
}

func TestMinNextBid(t *testing.T) {
	cases := []struct {
		name         string
		hasBids      bool
		start, price int64
		want         int64
	}{
		{"no bids returns start price", false, 1050, 1050, 1050},
		{"5 percent of 1000 is 50", true, 1000, 1000, 1050},
		{"ceil: 5 percent of 1001 is 51", true, 1000, 1001, 1052},
		{"minimum increment floor: 5 percent of 100 is 5 < 10", true, 100, 100, 110},
		{"minimum increment at boundary: 5 percent of 200 is exactly 10", true, 100, 200, 210},
		{"just above the boundary: 5 percent of 201 ceils to 11", true, 100, 201, 212},
		{"spec example", true, 1000, 1050, 1103}, // 1050 + ceil(52.5) = 1103
	}
	for _, c := range cases {
		if got := defaultRules.MinNextBid(c.hasBids, c.start, c.price); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}

func TestMinNextBidCustomRules(t *testing.T) {
	r := Rules{IncrementPercent: 10, IncrementMin: 1}
	if got := r.MinNextBid(true, 10, 15); got != 17 { // ceil(1.5) = 2
		t.Fatalf("got %d, want 17", got)
	}
	if got := r.MinNextBid(true, 1, 1); got != 2 { // ceil(0.1) = 1, floor of 1
		t.Fatalf("got %d, want 2", got)
	}
}

func TestBandsOf(t *testing.T) {
	b := defaultRules.Bands // 2m, 5m, 10m, 20m
	cases := []struct {
		remaining time.Duration
		want      Band
	}{
		{0, BandVeryShort},
		{119 * time.Second, BandVeryShort},
		{2 * time.Minute, BandShort}, // thresholds are exclusive upper bounds
		{299 * time.Second, BandShort},
		{5 * time.Minute, BandMedium},
		{9*time.Minute + 59*time.Second, BandMedium},
		{10 * time.Minute, BandLong},
		{19*time.Minute + 59*time.Second, BandLong},
		{20 * time.Minute, BandVeryLong},
		{3 * time.Hour, BandVeryLong},
	}
	for _, c := range cases {
		if got := b.Of(c.remaining); got != c.want {
			t.Errorf("Of(%v) = %s, want %s", c.remaining, got, c.want)
		}
	}
}
