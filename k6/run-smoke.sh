#!/usr/bin/env bash
# make k6-smoke: run the 50 VU bidding smoke test against an isolated scratch
# database and its own server container, then run reconcile on that database.
# Needs only Docker Compose; k6 itself runs in the grafana/k6 image.
#
# Environment (all optional):
#   SMOKE_INCREMENT_PERCENT bid increment percent for the run (default 0)
#   SMOKE_INCREMENT_MIN     minimum increment for the run (default 10)
#   KEEP_DB=1               keep the scratch database afterwards
#
# The default increment is linear (+10 per bid) on purpose: with the production
# default of 5 percent the price grows exponentially and reaches any player's
# balance after a few hundred accepted bids, after which the run only measures
# "insufficient gold" rejections. See docs/benchmarks.md.
set -euo pipefail
cd "$(dirname "$0")/.."

DC="docker compose"
SERVER=ah-smoke-server
mkdir -p k6/out

$DC up -d --build --wait postgres migrate >/dev/null 2>&1 || $DC up -d --build --wait postgres
$DC build tools server >/dev/null

eval "$($DC run --rm --no-deps -T tools k6prep create)"
cleanup() {
  docker rm -f "$SERVER" >/dev/null 2>&1 || true
  if [ "${KEEP_DB:-0}" != "1" ]; then
    $DC run --rm --no-deps -T tools k6prep drop "$SMOKE_DB_NAME" >/dev/null 2>&1 || true
  else
    echo "kept database $SMOKE_DB_NAME"
  fi
}
trap cleanup EXIT

echo "scratch database: $SMOKE_DB_NAME, auction $SMOKE_AUCTION_ID"
docker rm -f "$SERVER" >/dev/null 2>&1 || true
$DC run -d --no-deps --name "$SERVER" \
  -e DATABASE_URL="$SMOKE_DATABASE_URL" -e APP_ENV=dev \
  -e BID_INCREMENT_PERCENT="${SMOKE_INCREMENT_PERCENT:-0}" -e BID_INCREMENT_MIN="${SMOKE_INCREMENT_MIN:-10}" \
  server >/dev/null

for _ in $(seq 1 60); do
  [ "$(docker inspect -f '{{.State.Health.Status}}' "$SERVER" 2>/dev/null)" = "healthy" ] && break
  sleep 1
done

echo "--- environment"
docker version -f 'docker {{.Server.Version}} on {{.Server.Os}}/{{.Server.Arch}}'
docker run --rm alpine:3.22 sh -c 'uname -sr; grep -m1 "model name" /proc/cpuinfo; echo "logical cpus: $(nproc)"; awk "/MemTotal/ {print \"memory: \" int(\$2/1024) \" MB\"}" /proc/meminfo'
echo "increment: ${SMOKE_INCREMENT_PERCENT:-0}% / min ${SMOKE_INCREMENT_MIN:-10}"

echo "--- k6"
$DC run --rm --no-deps -T \
  -e BASE_URL="http://$SERVER:8080" -e AUCTION_ID="$SMOKE_AUCTION_ID" -e USER_IDS="$SMOKE_USER_IDS" \
  k6 run /work/k6/smoke.js

echo "--- reconcile"
$DC run --rm --no-deps -T -e DATABASE_URL="$SMOKE_DATABASE_URL" tools reconcile
