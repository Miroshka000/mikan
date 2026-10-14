-- name: SetInboundAuto :exec
-- The switches do not change what clients get, so updated_at stays.
UPDATE inbounds SET auto_port = $1, auto_sni = $2 WHERE id = $3;

-- name: RecordSubFetch :exec
INSERT INTO sub_fetches (user_id, ip, fetched_at) VALUES ($1, $2, $3)
ON CONFLICT (user_id, ip) DO UPDATE SET fetched_at = excluded.fetched_at;

-- name: ListSubFetchesSince :many
SELECT * FROM sub_fetches WHERE fetched_at >= $1;

-- name: PruneSubFetches :exec
DELETE FROM sub_fetches WHERE fetched_at < $1;

-- name: AddInboundEvent :exec
INSERT INTO inbound_events (inbound_id, node_id, kind, network, old_value, new_value, reason, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: LastInboundEvents :many
-- The latest automatic change of every inbound that had one.
SELECT * FROM inbound_events WHERE id IN (SELECT MAX(id) FROM inbound_events GROUP BY inbound_id);

-- name: ListInboundEventsSince :many
SELECT * FROM inbound_events WHERE created_at >= $1 ORDER BY id;

-- name: PruneInboundEvents :exec
DELETE FROM inbound_events WHERE created_at < $1;

-- name: UpsertInboundReaches :exec
-- When devices got through to inbounds, in one statement; each (slot, inbound) at most
-- once. A reach only moves forward.
INSERT INTO inbound_reach (slot, inbound_id, at)
SELECT t.slot, t.inbound_id, t.at
FROM (SELECT unnest(sqlc.arg(slots)::text[]) AS slot, unnest(sqlc.arg(inbound_ids)::bigint[]) AS inbound_id,
             unnest(sqlc.arg(ats)::bigint[]) AS at) AS t
ORDER BY t.slot, t.inbound_id
ON CONFLICT (slot, inbound_id) DO UPDATE SET at = GREATEST(inbound_reach.at, excluded.at);

-- name: ListInboundReachSince :many
SELECT * FROM inbound_reach WHERE at >= $1;

-- name: PruneInboundReach :exec
DELETE FROM inbound_reach WHERE at < $1;

-- name: SetInboundListen :exec
-- The listen address is the node's business, clients get nothing new: updated_at stays.
UPDATE inbounds SET listen = $1 WHERE id = $2;
