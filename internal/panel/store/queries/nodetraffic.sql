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

-- name: NodeTrafficHourly :many
SELECT hour, up, down FROM node_traffic_hourly WHERE node_id = $1 AND hour >= $2 ORDER BY hour;

-- name: NodeTrafficDaily :many
SELECT day, up, down FROM node_traffic_daily WHERE node_id = $1 AND day >= $2 ORDER BY day;

-- name: SumNodesTrafficHourly :many
-- What each node carried since an hour, for the nodes that carried anything.
SELECT node_id, CAST(sum(up) AS BIGINT) AS up, CAST(sum(down) AS BIGINT) AS down
FROM node_traffic_hourly WHERE hour >= $1 GROUP BY node_id;

-- name: SumNodesTrafficDaily :many
SELECT node_id, CAST(sum(up) AS BIGINT) AS up, CAST(sum(down) AS BIGINT) AS down
FROM node_traffic_daily WHERE day >= $1 GROUP BY node_id;

-- name: PruneNodeTrafficHourly :exec
DELETE FROM node_traffic_hourly WHERE hour < $1;

-- name: PruneNodeTrafficDaily :exec
DELETE FROM node_traffic_daily WHERE day < $1;

-- name: FirstNodeTrafficDay :one
-- The day the per-node accounting has its first row: 0 when it has none yet.
SELECT CAST(COALESCE(min(day), 0) AS BIGINT) FROM node_traffic_daily;
