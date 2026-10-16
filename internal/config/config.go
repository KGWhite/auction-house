// Package config loads runtime configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds every tunable of the system. All durations and thresholds come
// from environment variables so that tests and benchmarks can compress time.
type Config struct {
	AppEnv      string
	HTTPAddr    string
	DatabaseURL string

	BidIncrementPercent int64
	BidIncrementMin     int64

	BandVeryShort time.Duration
	BandShort     time.Duration
	BandMedium    time.Duration
	BandLong      time.Duration

	AuctionMinDuration time.Duration
	AuctionMaxDuration time.Duration

	SettleInterval  time.Duration
	SettleBatchSize int

	RefreshInterval time.Duration
	ItemsPerBracket int
	BracketMin      int
	BracketMax      int
}

// Default returns the development defaults.
func Default() Config {
	return Config{
		AppEnv:              "dev",
		HTTPAddr:            ":8080",
		DatabaseURL:         "postgres://auction:auction@localhost:5432/auction?sslmode=disable",
		BidIncrementPercent: 5,
		BidIncrementMin:     10,
		BandVeryShort:       2 * time.Minute,
		BandShort:           5 * time.Minute,
		BandMedium:          10 * time.Minute,
		BandLong:            20 * time.Minute,
		AuctionMinDuration:  3 * time.Minute,
		AuctionMaxDuration:  25 * time.Minute,
		SettleInterval:      2 * time.Second,
		SettleBatchSize:     50,
		RefreshInterval:     5 * time.Minute,
		ItemsPerBracket:     8,
		BracketMin:          0,
		BracketMax:          5,
	}
}

// IsDev reports whether the development-only features (X-Dev-User-Id header,
// /api/dev/* and /api/users routes) are enabled.
func (c Config) IsDev() bool { return c.AppEnv == "dev" }

// Load reads the configuration from the process environment.
func Load() (Config, error) { return LoadFrom(os.Getenv) }

// LoadFrom reads the configuration using the given lookup function. An empty
// value falls back to the default.
func LoadFrom(getenv func(string) string) (Config, error) {
	c := Default()
	var errs []string

	str := func(key string, dst *string) {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			*dst = v
		}
	}
	i64 := func(key string, dst *int64) {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", key, err))
				return
			}
			*dst = n
		}
	}
	integer := func(key string, dst *int) {
		var n = int64(*dst)
		i64(key, &n)
		*dst = int(n)
	}
	dur := func(key string, dst *time.Duration) {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", key, err))
				return
			}
			*dst = d
		}
	}

	str("APP_ENV", &c.AppEnv)
	str("HTTP_ADDR", &c.HTTPAddr)
	str("DATABASE_URL", &c.DatabaseURL)
	i64("BID_INCREMENT_PERCENT", &c.BidIncrementPercent)
	i64("BID_INCREMENT_MIN", &c.BidIncrementMin)
	dur("BAND_VERY_SHORT", &c.BandVeryShort)
	dur("BAND_SHORT", &c.BandShort)
	dur("BAND_MEDIUM", &c.BandMedium)
	dur("BAND_LONG", &c.BandLong)
	dur("AUCTION_MIN_DURATION", &c.AuctionMinDuration)
	dur("AUCTION_MAX_DURATION", &c.AuctionMaxDuration)
	dur("SETTLE_INTERVAL", &c.SettleInterval)
	integer("SETTLE_BATCH_SIZE", &c.SettleBatchSize)
	dur("REFRESH_INTERVAL", &c.RefreshInterval)
	integer("ITEMS_PER_BRACKET", &c.ItemsPerBracket)
	integer("BRACKET_MIN", &c.BracketMin)
	integer("BRACKET_MAX", &c.BracketMax)

	if len(errs) > 0 {
		return c, fmt.Errorf("invalid configuration: %s", strings.Join(errs, "; "))
	}
	if err := c.Validate(); err != nil {
		return c, err
	}
	return c, nil
}

// Validate checks cross-field constraints.
func (c Config) Validate() error {
	switch {
	case c.BidIncrementPercent < 0 || c.BidIncrementMin < 1:
		return fmt.Errorf("invalid configuration: bid increment must be percent>=0 and min>=1")
	case !(0 < c.BandVeryShort && c.BandVeryShort < c.BandShort && c.BandShort < c.BandMedium && c.BandMedium < c.BandLong):
		return fmt.Errorf("invalid configuration: BAND_* thresholds must be positive and strictly increasing")
	case c.AuctionMinDuration <= 0 || c.AuctionMinDuration > c.AuctionMaxDuration:
		return fmt.Errorf("invalid configuration: need 0 < AUCTION_MIN_DURATION <= AUCTION_MAX_DURATION")
	case c.SettleInterval <= 0 || c.SettleBatchSize <= 0 || c.RefreshInterval <= 0:
		return fmt.Errorf("invalid configuration: SETTLE_INTERVAL, SETTLE_BATCH_SIZE and REFRESH_INTERVAL must be positive")
	case c.ItemsPerBracket <= 0 || c.BracketMin < 0 || c.BracketMax < c.BracketMin:
		return fmt.Errorf("invalid configuration: need ITEMS_PER_BRACKET>0 and 0<=BRACKET_MIN<=BRACKET_MAX")
	}
	return nil
}
