-- Serialises refresh workers so that only one of them creates a given round.
-- name: LockRefresh :exec
SELECT pg_advisory_xact_lock(7001001);

-- True when the newest round is at least interval_ms old (database clock), or
-- when there is no round yet.
-- name: RefreshDue :one
SELECT ((now() - COALESCE(max(started_at), 'epoch'::timestamptz)) >= sqlc.arg(interval_ms)::bigint * interval '1 millisecond')::boolean AS due
FROM auction_rounds;

-- name: CountActiveAuctions :one
SELECT count(*) FROM auctions WHERE status = 'active';
