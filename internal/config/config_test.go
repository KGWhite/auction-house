package config

import (
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	c, err := LoadFrom(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c != Default() {
		t.Fatalf("expected defaults, got %+v", c)
	}
	if !c.IsDev() {
		t.Fatal("default APP_ENV should be dev")
	}
}

func TestLoadOverrides(t *testing.T) {
	c, err := LoadFrom(env(map[string]string{
		"APP_ENV":               "prod",
		"BID_INCREMENT_PERCENT": "7",
		"BAND_VERY_SHORT":       "10s",
		"BAND_SHORT":            "20s",
		"BAND_MEDIUM":           "30s",
		"BAND_LONG":             "40s",
		"AUCTION_MIN_DURATION":  "1s",
		"AUCTION_MAX_DURATION":  "2s",
		"SETTLE_BATCH_SIZE":     "9",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.IsDev() || c.BidIncrementPercent != 7 || c.BandVeryShort != 10*time.Second ||
		c.AuctionMaxDuration != 2*time.Second || c.SettleBatchSize != 9 {
		t.Fatalf("unexpected config %+v", c)
	}
}

func TestLoadInvalid(t *testing.T) {
	cases := []map[string]string{
		{"BID_INCREMENT_PERCENT": "abc"},
		{"BAND_SHORT": "1s"},           // not above BAND_VERY_SHORT
		{"AUCTION_MIN_DURATION": "1h"}, // min > max
		{"ITEMS_PER_BRACKET": "0"},
		{"BRACKET_MIN": "3", "BRACKET_MAX": "2"},
		{"SETTLE_INTERVAL": "nonsense"},
	}
	for i, m := range cases {
		if _, err := LoadFrom(env(m)); err == nil {
			t.Errorf("case %d: expected error for %v", i, m)
		}
	}
}
