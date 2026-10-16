package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"auctionhouse/internal/auction"
	"auctionhouse/internal/config"
	"auctionhouse/internal/db"
	"auctionhouse/internal/httpapi"
	"auctionhouse/internal/item"
	"auctionhouse/internal/testutil"
)

func TestMain(m *testing.M) { os.Exit(testutil.Run(m)) }

type harness struct {
	t       *testing.T
	pool    *pgxpool.Pool
	svc     *auction.Service
	handler http.Handler
}

func newHarness(t *testing.T, mutate ...func(*config.Config)) *harness {
	t.Helper()
	pool := testutil.Pool(t)
	cfg := config.Default()
	for _, m := range mutate {
		m(&cfg)
	}
	svc := auction.NewService(pool, auction.RulesFromConfig(cfg))
	h := httpapi.New(cfg, pool, svc, log.New(io.Discard, "", 0))
	return &harness{t: t, pool: pool, svc: svc, handler: h}
}

type response struct {
	status int
	raw    []byte
	t      *testing.T
}

func (r response) json() map[string]any {
	r.t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.raw, &m); err != nil {
		r.t.Fatalf("response is not a JSON object: %v\n%s", err, r.raw)
	}
	return m
}

func (r response) errorField(name string) any {
	r.t.Helper()
	e, ok := r.json()["error"].(map[string]any)
	if !ok {
		r.t.Fatalf("no error object in %s", r.raw)
	}
	return e[name]
}

func (h *harness) do(method, path string, user int64, body string, headers ...string) response {
	h.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if user != 0 {
		req.Header.Set(httpapi.DevUserHeader, fmt.Sprint(user))
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return response{status: rec.Code, raw: rec.Body.Bytes(), t: h.t}
}

func (h *harness) bid(user, auctionID int64, amount int64, key string) response {
	h.t.Helper()
	hdr := []string{}
	if key != "" {
		hdr = []string{"Idempotency-Key", key}
	}
	return h.do("POST", fmt.Sprintf("/api/auctions/%d/bids", auctionID), user, fmt.Sprintf(`{"amount": %d}`, amount), hdr...)
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestAuctionResponseShapeAndNoEndTime(t *testing.T) {
	h := newHarness(t)
	u := testutil.NewUser(t, h.pool, 15, 5000)
	a := testutil.NewAuction(t, h.pool, testutil.AuctionOpts{ItemLevel: 15, StartPrice: 1000, Duration: time.Hour})

	check := func(r response) map[string]any {
		t.Helper()
		if r.status != 200 {
			t.Fatalf("status %d: %s", r.status, r.raw)
		}
		if bytes.Contains(bytes.ToLower(r.raw), []byte("end_at")) || bytes.Contains(bytes.ToLower(r.raw), []byte("endat")) {
			t.Fatalf("response leaks the end time: %s", r.raw)
		}
		return r.json()
	}
	wantKeys := []string{"current_price", "has_bids", "i_have_bid", "id", "is_mine", "item", "level_bracket", "min_next_bid", "status", "time_band", "top_bidder_name"}

	single := check(h.do("GET", fmt.Sprintf("/api/auctions/%d", a), u, ""))
	if !reflect.DeepEqual(keys(single), wantKeys) {
		t.Fatalf("keys %v, want %v", keys(single), wantKeys)
	}
	if itemKeys := keys(single["item"].(map[string]any)); !reflect.DeepEqual(itemKeys, []string{"level", "name", "type"}) {
		t.Fatalf("item keys %v", itemKeys)
	}
	if single["current_price"] != 1000.0 || single["min_next_bid"] != 1000.0 || single["has_bids"] != false ||
		single["top_bidder_name"] != nil || single["is_mine"] != false || single["i_have_bid"] != false ||
		single["time_band"] != "very_long" || single["status"] != "active" || single["level_bracket"] != 1.0 {
		t.Fatalf("unexpected auction %v", single)
	}

	list := check(h.do("GET", "/api/auctions", u, ""))
	arr := list["auctions"].([]any)
	if len(arr) != 1 || !reflect.DeepEqual(keys(arr[0].(map[string]any)), wantKeys) {
		t.Fatalf("list %v", list)
	}
}

func TestViewerSpecificFlagsAndMinNextBid(t *testing.T) {
	h := newHarness(t)
	alice := testutil.NewUser(t, h.pool, 15, 5000)
	bob := testutil.NewUser(t, h.pool, 16, 5000)
	carol := testutil.NewUser(t, h.pool, 17, 5000)
	a := testutil.NewAuction(t, h.pool, testutil.AuctionOpts{StartPrice: 1000})

	if r := h.bid(alice, a, 1000, "a1"); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.raw)
	}
	get := func(viewer int64) map[string]any {
		return h.do("GET", fmt.Sprintf("/api/auctions/%d", a), viewer, "").json()
	}
	av := get(alice)
	if av["is_mine"] != true || av["i_have_bid"] != true || av["has_bids"] != true || av["min_next_bid"] != 1050.0 {
		t.Fatalf("alice sees %v", av)
	}
	if av["top_bidder_name"] == nil {
		t.Fatal("top bidder name missing")
	}
	if r := h.bid(bob, a, 1050, "b1"); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.raw)
	}
	av = get(alice) // outbid
	if av["is_mine"] != false || av["i_have_bid"] != true {
		t.Fatalf("outbid alice sees %v", av)
	}
	cv := get(carol) // never bid
	if cv["is_mine"] != false || cv["i_have_bid"] != false || cv["min_next_bid"] != 1103.0 || cv["current_price"] != 1050.0 {
		t.Fatalf("carol sees %v", cv)
	}
	anon := h.do("GET", fmt.Sprintf("/api/auctions/%d", a), 0, "")
	if anon.status != 200 || anon.json()["is_mine"] != false {
		t.Fatalf("anonymous: %d %s", anon.status, anon.raw)
	}
}

func TestTimeBands(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		remaining time.Duration
		band      string
	}{
		{30 * time.Second, "very_short"},
		{3 * time.Minute, "short"},
		{7 * time.Minute, "medium"},
		{15 * time.Minute, "long"},
		{time.Hour, "very_long"},
	}
	for _, c := range cases {
		id := testutil.NewAuction(t, h.pool, testutil.AuctionOpts{Duration: c.remaining})
		got := h.do("GET", fmt.Sprintf("/api/auctions/%d", id), 0, "").json()["time_band"]
		if got != c.band {
			t.Errorf("remaining %v: band %v, want %s", c.remaining, got, c.band)
		}
	}
}

func TestBandsFollowConfiguredThresholds(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.BandVeryShort, c.BandShort, c.BandMedium, c.BandLong = time.Second*10, time.Second*20, time.Second*30, time.Second*40
	})
	id := testutil.NewAuction(t, h.pool, testutil.AuctionOpts{Duration: 25 * time.Second})
	if got := h.do("GET", fmt.Sprintf("/api/auctions/%d", id), 0, "").json()["time_band"]; got != "medium" {
		t.Fatalf("band %v, want medium", got)
	}
}

func TestListFiltersAndSorting(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	cheap := testutil.NewAuction(t, h.pool, testutil.AuctionOpts{ItemLevel: 5, StartPrice: 300})
	mid := testutil.NewAuction(t, h.pool, testutil.AuctionOpts{ItemLevel: 15, StartPrice: 900})
	dear := testutil.NewAuction(t, h.pool, testutil.AuctionOpts{ItemLevel: 25, StartPrice: 2000})
	if _, err := h.pool.Exec(ctx, `UPDATE item_templates SET type='ring' WHERE id = (SELECT i.template_id FROM auctions a JOIN items i ON i.id=a.item_id WHERE a.id=$1)`, mid); err != nil {
		t.Fatal(err)
	}
	settledOne := testutil.NewAuction(t, h.pool, testutil.AuctionOpts{})
	if _, err := h.pool.Exec(ctx, `UPDATE auctions SET status='settled' WHERE id=$1`, settledOne); err != nil {
		t.Fatal(err)
	}

	ids := func(path string) []int64 {
		t.Helper()
		r := h.do("GET", path, 0, "")
		if r.status != 200 {
			t.Fatalf("%s: %d %s", path, r.status, r.raw)
		}
		var out []int64
		for _, a := range r.json()["auctions"].([]any) {
			out = append(out, int64(a.(map[string]any)["id"].(float64)))
		}
		return out
	}
	eq := func(got, want []int64) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	eq(ids("/api/auctions?bracket=1"), []int64{mid})
	eq(ids("/api/auctions?bracket=0"), []int64{cheap})
	eq(ids("/api/auctions?type=ring"), []int64{mid})
	eq(ids("/api/auctions?sort=price_desc"), []int64{dear, mid, cheap})
	eq(ids("/api/auctions?sort=price_asc"), []int64{cheap, mid, dear})
	eq(ids("/api/auctions?sort=level_desc"), []int64{dear, mid, cheap})
	if got := ids("/api/auctions"); len(got) != 3 { // settled auctions are not listed by default
		t.Fatalf("default list %v", got)
	}

	for _, bad := range []string{"/api/auctions?bracket=x", "/api/auctions?bracket=-1", "/api/auctions?type=sword", "/api/auctions?sort=random"} {
		if r := h.do("GET", bad, 0, ""); r.status != 400 || r.errorField("code") != "invalid_request" {
			t.Errorf("%s: %d %s", bad, r.status, r.raw)
		}
	}
	// A settled auction can still be fetched directly.
	if r := h.do("GET", fmt.Sprintf("/api/auctions/%d", settledOne), 0, ""); r.status != 200 || r.json()["status"] != "settled" {
		t.Fatalf("%d %s", r.status, r.raw)
	}
}

func TestBidSuccessAndIdempotentReplay(t *testing.T) {
	h := newHarness(t)
	u := testutil.NewUser(t, h.pool, 15, 5000)
	a := testutil.NewAuction(t, h.pool, testutil.AuctionOpts{StartPrice: 1000})

	r := h.bid(u, a, 1000, "key-1")
	if r.status != 200 {
		t.Fatalf("%d %s", r.status, r.raw)
	}
	b := r.json()
	if b["amount"] != 1000.0 || b["current_price"] != 1000.0 || b["min_next_bid"] != 1050.0 || b["replayed"] != false || b["auction_id"] != float64(a) {
		t.Fatalf("body %v", b)
	}
	again := h.bid(u, a, 1000, "key-1")
	if again.status != 200 || again.json()["replayed"] != true || again.json()["bid_id"] != b["bid_id"] {
		t.Fatalf("replay: %d %s", again.status, again.raw)
	}
	me := h.do("GET", "/api/me", u, "").json()
	if me["gold_available"] != 4000.0 || me["gold_frozen"] != 1000.0 {
		t.Fatalf("me %v", me)
	}
}

func TestBidErrorMapping(t *testing.T) {
	h := newHarness(t)
	alice := testutil.NewUser(t, h.pool, 15, 5000)
	bob := testutil.NewUser(t, h.pool, 16, 1100)
	low := testutil.NewUser(t, h.pool, 5, 5000)
	a := testutil.NewAuction(t, h.pool, testutil.AuctionOpts{ItemLevel: 15, StartPrice: 1000})
	if r := h.bid(alice, a, 1000, "a1"); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.raw)
	}
	ended := testutil.NewAuction(t, h.pool, testutil.AuctionOpts{ItemLevel: 15, Duration: -time.Second})

	type tc struct {
		name   string
		resp   response
		status int
		code   string
	}
	cases := []tc{
		{"too low", h.bid(bob, a, 1049, "b1"), 409, "bid_too_low"},
		{"already top bidder", h.bid(alice, a, 5000, "a2"), 409, "already_top_bidder"},
		{"auction ended", h.bid(alice, ended, 1000, "a3"), 410, "auction_ended"},
		{"insufficient gold", h.bid(bob, a, 1200, "b2"), 422, "insufficient_gold"},
		{"level bracket mismatch", h.bid(low, a, 1050, "l1"), 422, "level_bracket_mismatch"},
		{"auction not found", h.bid(alice, 987654, 1000, "a4"), 404, "auction_not_found"},
		{"missing idempotency key", h.bid(alice, a, 1100, ""), 400, "missing_idempotency_key"},
		{"no identity header", h.do("POST", fmt.Sprintf("/api/auctions/%d/bids", a), 0, `{"amount":1100}`, "Idempotency-Key", "x"), 401, "unauthorized"},
		{"unknown player", h.bid(424242, a, 1100, "u1"), 401, "unauthorized"},
	}
	for _, c := range cases {
		if c.resp.status != c.status || c.resp.errorField("code") != c.code || c.resp.errorField("message") == "" {
			t.Errorf("%s: got %d %s, want %d %s", c.name, c.resp.status, c.resp.raw, c.status, c.code)
		}
	}

	// bid_too_low carries the new minimum in the documented position.
	tooLow := cases[0].resp
	if tooLow.errorField("min_next_bid") != 1050.0 {
		t.Errorf("min_next_bid = %v in %s", tooLow.errorField("min_next_bid"), tooLow.raw)
	}
	if cases[1].resp.errorField("min_next_bid") != nil {
		t.Error("only bid_too_low carries min_next_bid")
	}
}

func TestBidBodyValidation(t *testing.T) {
	h := newHarness(t)
	u := testutil.NewUser(t, h.pool, 15, 5000)
	a := testutil.NewAuction(t, h.pool, testutil.AuctionOpts{StartPrice: 1000})
	path := fmt.Sprintf("/api/auctions/%d/bids", a)
	for _, body := range []string{``, `not json`, `{}`, `{"amount": "1000"}`, `{"amount": 1000.5}`, `{"amount": null}`, `{"amount": 1000} {"amount": 2000}`, `[1000]`} {
		r := h.do("POST", path, u, body, "Idempotency-Key", "k-"+body)
		if r.status != 400 || r.errorField("code") != "invalid_request" {
			t.Errorf("body %q: %d %s", body, r.status, r.raw)
		}
	}
	if r := h.do("POST", path, u, `{"amount": 1000}`, "Idempotency-Key", strings.Repeat("k", 201)); r.status != 400 {
		t.Errorf("oversized key: %d %s", r.status, r.raw)
	}
	if r := h.do("POST", "/api/auctions/abc/bids", u, `{"amount": 1000}`, "Idempotency-Key", "k"); r.status != 404 {
		t.Errorf("non-numeric id: %d", r.status)
	}
}

func TestMeInboxAndLedger(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	alice := testutil.NewUser(t, h.pool, 15, 10_000)
	if r := h.do("GET", "/api/me", 0, ""); r.status != 401 || r.errorField("code") != "unauthorized" {
		t.Fatalf("anonymous /api/me: %d %s", r.status, r.raw)
	}
	if r := h.do("GET", "/api/me", 99999, ""); r.status != 401 {
		t.Fatalf("unknown /api/me: %d", r.status)
	}
	if r := h.do("GET", "/api/me", alice, "", httpapi.DevUserHeader, "not-a-number"); r.status != 401 {
		t.Fatalf("bad header: %d", r.status)
	}

	me := h.do("GET", "/api/me", alice, "").json()
	if me["id"] != float64(alice) || me["level"] != 15.0 || me["level_bracket"] != 1.0 || me["gold_available"] != 10000.0 || me["gold_frozen"] != 0.0 || me["inbox_count"] != 0.0 {
		t.Fatalf("me %v", me)
	}

	// Win an item: bid, end, settle.
	a := testutil.NewAuction(t, h.pool, testutil.AuctionOpts{StartPrice: 1000})
	if r := h.bid(alice, a, 1000, "w1"); r.status != 200 {
		t.Fatalf("%d %s", r.status, r.raw)
	}
	testutil.SetEndIn(t, h.pool, a, -time.Second)
	if _, err := h.svc.SettleDue(ctx, 10); err != nil {
		t.Fatal(err)
	}
	me = h.do("GET", "/api/me", alice, "").json()
	if me["gold_available"] != 9000.0 || me["gold_frozen"] != 0.0 || me["inbox_count"] != 1.0 {
		t.Fatalf("me after win %v", me)
	}
	inbox := h.do("GET", "/api/me/inbox", alice, "").json()["items"].([]any)
	if len(inbox) != 1 {
		t.Fatalf("inbox %v", inbox)
	}
	entry := inbox[0].(map[string]any)
	it := entry["item"].(map[string]any)
	if entry["price_paid"] != 1000.0 || entry["auction_id"] != float64(a) || it["name"] == "" || it["level"] != 15.0 || entry["message"] == "" || entry["created_at"] == nil {
		t.Fatalf("inbox entry %v", entry)
	}

	// Ledger: grant, freeze, spend (newest first) with keyset pagination.
	ledger := h.do("GET", "/api/me/ledger", alice, "").json()
	entries := ledger["entries"].([]any)
	var types []string
	for _, e := range entries {
		types = append(types, e.(map[string]any)["entry_type"].(string))
	}
	if !reflect.DeepEqual(types, []string{"spend", "freeze", "grant"}) || ledger["next_before_id"] != nil {
		t.Fatalf("ledger %v", ledger)
	}
	page1 := h.do("GET", "/api/me/ledger?limit=2", alice, "").json()
	if len(page1["entries"].([]any)) != 2 || page1["next_before_id"] == nil {
		t.Fatalf("page1 %v", page1)
	}
	page2 := h.do("GET", fmt.Sprintf("/api/me/ledger?limit=2&before_id=%d", int64(page1["next_before_id"].(float64))), alice, "").json()
	p2 := page2["entries"].([]any)
	if len(p2) != 1 || p2[0].(map[string]any)["entry_type"] != "grant" || page2["next_before_id"] != nil {
		t.Fatalf("page2 %v", page2)
	}
	// Another player's ledger is not visible.
	bob := testutil.NewUser(t, h.pool, 15, 1)
	if n := len(h.do("GET", "/api/me/ledger", bob, "").json()["entries"].([]any)); n != 1 {
		t.Fatalf("bob sees %d ledger entries, want his own 1", n)
	}
	for _, bad := range []string{"limit=0", "limit=201", "limit=x", "before_id=-1", "before_id=x"} {
		if r := h.do("GET", "/api/me/ledger?"+bad, alice, ""); r.status != 400 {
			t.Errorf("%s: %d", bad, r.status)
		}
	}
}

func TestDevEndpoints(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	a := testutil.NewUser(t, h.pool, 5, 100)
	b := testutil.NewUser(t, h.pool, 48, 100)
	users := h.do("GET", "/api/users", 0, "").json()["users"].([]any)
	if len(users) != 2 {
		t.Fatalf("users %v", users)
	}
	first := users[0].(map[string]any)
	if first["id"] != float64(a) || first["level"] != 5.0 || first["level_bracket"] != 0.0 || users[1].(map[string]any)["id"] != float64(b) {
		t.Fatalf("users %v", users)
	}
	if _, ok := first["gold_available"]; ok {
		t.Fatal("the user list must not expose balances")
	}

	if _, err := item.SeedTemplates(ctx, db.New(h.pool)); err != nil {
		t.Fatal(err)
	}
	r := h.do("POST", "/api/dev/refresh", 0, "")
	if r.status != 200 || r.json()["auctions"] != 48.0 {
		t.Fatalf("refresh: %d %s", r.status, r.raw)
	}
	if n := len(h.do("GET", "/api/auctions", 0, "").json()["auctions"].([]any)); n != 48 {
		t.Fatalf("%d auctions after refresh", n)
	}
}

func TestDevFeaturesAreOffOutsideDevEnvironment(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.AppEnv = "prod" })
	u := testutil.NewUser(t, h.pool, 15, 5000)
	a := testutil.NewAuction(t, h.pool, testutil.AuctionOpts{})

	if r := h.do("GET", "/api/users", 0, ""); r.status != 404 {
		t.Errorf("/api/users: %d", r.status)
	}
	if r := h.do("POST", "/api/dev/refresh", 0, ""); r.status != 404 {
		t.Errorf("/api/dev/refresh: %d", r.status)
	}
	// The identity header is ignored: bidding and /api/me need real auth, which does not exist yet.
	if r := h.do("GET", "/api/me", u, ""); r.status != 401 {
		t.Errorf("/api/me with dev header: %d", r.status)
	}
	if r := h.bid(u, a, 1000, "k"); r.status != 401 {
		t.Errorf("bid with dev header: %d", r.status)
	}
	// Browsing stays public.
	if r := h.do("GET", "/api/auctions", u, ""); r.status != 200 {
		t.Errorf("list: %d", r.status)
	}
}

func TestUnknownRoutesUseTheErrorFormat(t *testing.T) {
	h := newHarness(t)
	r := h.do("GET", "/api/nope", 0, "")
	if r.status != 404 || r.errorField("code") != "not_found" {
		t.Fatalf("%d %s", r.status, r.raw)
	}
	if got := h.do("GET", "/api/auctions/999999", 0, ""); got.status != 404 || got.errorField("code") != "auction_not_found" {
		t.Fatalf("%d %s", got.status, got.raw)
	}
	// A method with no handler falls through to the catch-all, so it also gets the JSON 404.
	if got := h.do("DELETE", "/api/auctions/1", 0, ""); got.status != 404 || got.errorField("code") != "not_found" {
		t.Fatalf("%d %s", got.status, got.raw)
	}
}

func TestInvariantsHoldAfterHTTPBidding(t *testing.T) {
	h := newHarness(t)
	alice := testutil.NewUser(t, h.pool, 15, 50_000)
	bob := testutil.NewUser(t, h.pool, 16, 50_000)
	a := testutil.NewAuction(t, h.pool, testutil.AuctionOpts{StartPrice: 1000})
	next := int64(1000)
	for i := 0; i < 10; i++ {
		u := alice
		if i%2 == 1 {
			u = bob
		}
		r := h.bid(u, a, next, fmt.Sprintf("k%d", i))
		if r.status != 200 {
			t.Fatalf("bid %d: %d %s", i, r.status, r.raw)
		}
		next = int64(r.json()["min_next_bid"].(float64))
	}
	testutil.RequireInvariants(t, h.pool)
}
