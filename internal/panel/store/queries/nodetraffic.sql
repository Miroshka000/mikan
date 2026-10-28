-- name: AddNodeTraffic :exec
-- What a node carried in a batch: its hour, its day and its running total, in one
-- statement. The node is kept from being deleted until the transaction ends; one that is
-- gone already is skipped, the rest of the batch still counts.
WITH n AS (SELECT id FROM nodes WHERE id = sqlc.arg(node_id)::bigint FOR KEY SHARE),
h AS (
  INSERT INTO node_traffic_hourly (node_id, hour, up, down)
  SELECT n.id, sqlc.arg(hour)::bigint, sqlc.arg(up)::bigint, sqlc.arg(down)::bigint FROM n
  ON CONFLICT (node_id, hour) DO UPDATE SET up = node_traffic_hourly.up + excluded.up, down = node_traffic_hourly.down + excluded.down
), d AS (
  INSERT INTO node_traffic_daily (node_id, day, up, down)
  SELECT n.id, sqlc.arg(day)::bigint, sqlc.arg(up)::bigint, sqlc.arg(down)::bigint FROM n
  ON CONFLICT (node_id, day) DO UPDATE SET up = node_traffic_daily.up + excluded.up, down = node_traffic_daily.down + excluded.down
)
UPDATE nodes SET total_up = total_up + sqlc.arg(up)::bigint, total_down = total_down + sqlc.arg(down)::bigint
WHERE id IN (SELECT id FROM n);

-- name: AddNodeRelayTraffic :exec
-- What a node's relay carried for the other nodes' cascades in a batch: its hour and its
-- day, in the relay's columns. The source nodes counted these bytes for their users, so
-- up, down and the node's running total (the users' bytes) do not take them. A node gone
-- already is skipped.
WITH n AS (SELECT id FROM nodes WHERE id = sqlc.arg(node_id)::bigint FOR KEY SHARE),
h AS (
  INSERT INTO node_traffic_hourly (node_id, hour, relay_up, relay_down)
  SELECT n.id, sqlc.arg(hour)::bigint, sqlc.arg(up)::bigint, sqlc.arg(down)::bigint FROM n
  ON CONFLICT (node_id, hour) DO UPDATE SET relay_up = node_traffic_hourly.relay_up + excluded.relay_up, relay_down = node_traffic_hourly.relay_down + excluded.relay_down
)
INSERT INTO node_traffic_daily (node_id, day, relay_up, relay_down)
SELECT n.id, sqlc.arg(day)::bigint, sqlc.arg(up)::bigint, sqlc.arg(down)::bigint FROM n
ON CONFLICT (node_id, day) DO UPDATE SET relay_up = node_traffic_daily.relay_up + excluded.relay_up, relay_down = node_traffic_daily.relay_down + excluded.relay_down;

-- name: NodeTrafficHourly :many
SELECT hour, up, down, relay_up, relay_down FROM node_traffic_hourly WHERE node_id = $1 AND hour >= $2 ORDER BY hour;

-- name: NodeTrafficDaily :many
SELECT day, up, down, relay_up, relay_down FROM node_traffic_daily WHERE node_id = $1 AND day >= $2 ORDER BY day;

-- name: NodeTrafficSinceDay :one
-- Everything a node carried since a day: its users' bytes and its relay's for the other
-- nodes' cascades, both ways. It is the server's traffic its provider counts.
SELECT CAST(COALESCE(sum(up + down + relay_up + relay_down), 0) AS BIGINT) FROM node_traffic_daily
WHERE node_id = $1 AND day >= $2;

-- name: SumNodesTrafficHourly :many
-- What each node carried since an hour, for the nodes that carried anything: its users'
-- bytes, and apart from them its relay's for the other nodes' cascades.
SELECT node_id, CAST(sum(up) AS BIGINT) AS up, CAST(sum(down) AS BIGINT) AS down,
  CAST(sum(relay_up) AS BIGINT) AS relay_up, CAST(sum(relay_down) AS BIGINT) AS relay_down
FROM node_traffic_hourly WHERE hour >= $1 GROUP BY node_id;

-- name: SumNodesTrafficDaily :many
SELECT node_id, CAST(sum(up) AS BIGINT) AS up, CAST(sum(down) AS BIGINT) AS down,
  CAST(sum(relay_up) AS BIGINT) AS relay_up, CAST(sum(relay_down) AS BIGINT) AS relay_down
FROM node_traffic_daily WHERE day >= $1 GROUP BY node_id;

-- name: PruneNodeTrafficHourly :exec
DELETE FROM node_traffic_hourly WHERE hour < $1;

-- name: PruneNodeTrafficDaily :exec
DELETE FROM node_traffic_daily WHERE day < $1;

-- name: FirstNodeTrafficDay :one
-- The day the per-node accounting has its first row: 0 when it has none yet.
SELECT CAST(COALESCE(min(day), 0) AS BIGINT) FROM node_traffic_daily;
