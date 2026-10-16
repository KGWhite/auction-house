// Smoke / load test: 50 virtual users keep bidding on ONE auction for 30 seconds.
//
// Every request carries a fresh Idempotency-Key. Each VU is a different player
// and bids the min_next_bid it learned from its last response (a 409
// bid_too_low carries the new minimum; after "already top bidder" the VU
// re-reads the auction), so the price climbs for the whole run.
//
// Required environment (set by k6/run-smoke.sh):
//   BASE_URL     e.g. http://localhost:18080
//   AUCTION_ID   the auction everyone bids on
//   USER_IDS     comma separated player ids (at least 50, all in the auction's bracket)
import http from 'k6/http'
import { sleep } from 'k6'
import { Counter, Trend } from 'k6/metrics'

const BASE_URL = __ENV.BASE_URL || 'http://localhost:18080'
const AUCTION_ID = __ENV.AUCTION_ID
const USER_IDS = (__ENV.USER_IDS || '').split(',').filter(Boolean)
const VUS = Number(__ENV.VUS || 50)
const DURATION = __ENV.DURATION || '30s'

export const options = {
  scenarios: {
    smoke: { executor: 'constant-vus', vus: VUS, duration: DURATION },
  },
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
}

// 200/409/410/422 are normal business outcomes of a bid, not failures.
http.setResponseCallback(http.expectedStatuses(200, 409, 410, 422))

const bidLatency = new Trend('bid_latency', true)
const bids = {
  200: new Counter('bid_status_200'),
  409: new Counter('bid_status_409'),
  410: new Counter('bid_status_410'),
  422: new Counter('bid_status_422'),
  other: new Counter('bid_status_other'),
}
const bidsAccepted = new Counter('bids_accepted')

let minNext = 0 // per VU: each VU has its own JS runtime
let seq = 0

function key() {
  seq += 1
  return `smoke-${__VU}-${__ITER}-${seq}-${Math.random().toString(36).slice(2, 10)}`
}

function userHeaders(extra) {
  const user = USER_IDS[(__VU - 1) % USER_IDS.length]
  return Object.assign({ 'X-Dev-User-Id': user, 'Content-Type': 'application/json' }, extra || {})
}

function refreshMinNext() {
  const res = http.get(`${BASE_URL}/api/auctions/${AUCTION_ID}`, { headers: userHeaders(), tags: { name: 'GET auction' } })
  if (res.status === 200) minNext = res.json('min_next_bid')
}

export function setup() {
  if (!AUCTION_ID || USER_IDS.length < VUS) {
    throw new Error(`need AUCTION_ID and at least ${VUS} USER_IDS (got ${USER_IDS.length})`)
  }
}

export default function () {
  if (minNext === 0) refreshMinNext()

  const res = http.post(`${BASE_URL}/api/auctions/${AUCTION_ID}/bids`, JSON.stringify({ amount: minNext }), {
    headers: userHeaders({ 'Idempotency-Key': key() }),
    tags: { name: 'POST bid' },
  })
  bidLatency.add(res.timings.duration)
  const counter = bids[res.status] || bids.other
  counter.add(1)

  if (res.status === 200) {
    bidsAccepted.add(1)
    minNext = res.json('min_next_bid')
  } else if (res.status === 409) {
    const min = res.json('error.min_next_bid')
    if (min) {
      minNext = min // bid_too_low: adopt the new minimum
    } else {
      // already_top_bidder: wait for someone else to outbid us, then re-read the price.
      sleep(0.02)
      refreshMinNext()
    }
  } else if (res.status === 422) {
    // Not enough gold or wrong bracket: nothing sensible to retry with.
    sleep(0.05)
    refreshMinNext()
  } else if (res.status === 410) {
    sleep(0.5)
  }
}

function pct(m, q) {
  return m && m.values && m.values[q] !== undefined ? m.values[q] : null
}

export function handleSummary(data) {
  const m = data.metrics
  const count = (name) => (m[name] ? m[name].values.count : 0)
  const total = count('bid_status_200') + count('bid_status_409') + count('bid_status_410') + count('bid_status_422') + count('bid_status_other')
  const share = (n) => (total ? ((100 * n) / total).toFixed(1) + '%' : 'n/a')
  const secs = data.state && data.state.testRunDurationMs ? data.state.testRunDurationMs / 1000 : 30
  const lat = m.bid_latency
  const fmt = (v) => (v === null ? 'n/a' : v.toFixed(2) + ' ms')

  const out = {
    vus: VUS,
    duration_s: secs,
    bid_requests: total,
    bid_requests_per_s: total / secs,
    http_requests: count('http_reqs'),
    bids_accepted: count('bids_accepted'),
    bids_accepted_per_s: count('bids_accepted') / secs,
    status: {
      200: count('bid_status_200'),
      409: count('bid_status_409'),
      410: count('bid_status_410'),
      422: count('bid_status_422'),
      other: count('bid_status_other'),
    },
    latency_ms: {
      avg: pct(lat, 'avg'),
      p50: pct(lat, 'med'),
      p90: pct(lat, 'p(90)'),
      p95: pct(lat, 'p(95)'),
      p99: pct(lat, 'p(99)'),
      max: pct(lat, 'max'),
    },
  }

  const text = [
    '',
    `k6 smoke: ${VUS} VUs x ${secs.toFixed(1)}s on auction ${AUCTION_ID}`,
    `  bid requests      ${total}  (${out.bid_requests_per_s.toFixed(1)} req/s)`,
    `  accepted bids     ${out.bids_accepted}  (${out.bids_accepted_per_s.toFixed(1)} /s)`,
    `  latency (POST bid) avg ${fmt(out.latency_ms.avg)}  p50 ${fmt(out.latency_ms.p50)}  p95 ${fmt(out.latency_ms.p95)}  p99 ${fmt(out.latency_ms.p99)}  max ${fmt(out.latency_ms.max)}`,
    `  status 200 ${share(out.status[200])}  409 ${share(out.status[409])}  410 ${share(out.status[410])}  422 ${share(out.status[422])}  other ${share(out.status.other)}`,
    '',
  ].join('\n')

  return {
    stdout: text,
    'k6/out/smoke-summary.json': JSON.stringify(out, null, 2),
  }
}
