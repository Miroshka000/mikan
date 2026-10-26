-- Server groups (traffic_pools): a group of inbounds that a tariff and a subscription hold
-- in one mode each (0044_server_groups.sql): main, own, unlimited or closed.

-- name: ListTrafficPools :many
SELECT * FROM traffic_pools ORDER BY name;

-- name: GetTrafficPool :one
SELECT * FROM traffic_pools WHERE id = $1;

-- name: CreateTrafficPool :one
INSERT INTO traffic_pools (name, created_at) VALUES ($1, $2) RETURNING *;

-- name: RenameTrafficPool :execrows
UPDATE traffic_pools SET name = $1 WHERE id = $2;

-- name: DeleteTrafficPool :execrows
DELETE FROM traffic_pools WHERE id = $1;

-- name: SetInboundPool :exec
UPDATE inbounds SET pool_id = $1 WHERE id = $2;

-- name: SetInboundNoAuto :exec
UPDATE inbounds SET no_auto = $1 WHERE id = $2;

-- name: SetGroupInbounds :execrows
-- The group's inbounds become exactly these: the ones listed join it (leaving the group
-- they were in), the ones it had and the list leaves out go back to the main servers.
UPDATE inbounds SET pool_id = CASE WHEN id = ANY(sqlc.arg(ids)::bigint[]) THEN sqlc.arg(pool_id)::BIGINT ELSE NULL END
WHERE id = ANY(sqlc.arg(ids)::bigint[]) OR pool_id = sqlc.arg(pool_id)::BIGINT;

-- name: ListTariffPools :many
SELECT * FROM tariff_pools WHERE tariff_id = $1 ORDER BY pool_id;

-- name: ListAllTariffPools :many
SELECT * FROM tariff_pools ORDER BY tariff_id, pool_id;

-- name: ClearTariffPools :exec
DELETE FROM tariff_pools WHERE tariff_id = $1;

-- name: AddTariffPool :exec
INSERT INTO tariff_pools (tariff_id, pool_id, mode, traffic_limit) VALUES ($1, $2, $3, $4);

-- name: ListUserIDsOfTariff :many
-- The users a tariff was given to, for bringing them up to the tariff.
SELECT id FROM users WHERE tariff_id = $1 ORDER BY id;

-- name: CountUsersByTariff :many
-- How many subscriptions each tariff was given to.
SELECT CAST(tariff_id AS BIGINT) AS tariff_id, COUNT(*) AS users FROM users WHERE tariff_id IS NOT NULL GROUP BY tariff_id;

-- name: ListUserPools :many
SELECT * FROM user_pools WHERE user_id = $1 ORDER BY pool_id;

-- name: ListAllUserPools :many
SELECT * FROM user_pools ORDER BY user_id, pool_id;

-- name: ResetUserPoolModes :exec
-- Every group of the user back in the main traffic, before a tariff's modes are written:
-- what the user used stays.
UPDATE user_pools SET mode = 'main', traffic_limit = NULL WHERE user_id = $1;

-- name: SetUserPool :exec
-- How a group is in the subscription; traffic_limit only with the mode own.
INSERT INTO user_pools (user_id, pool_id, mode, traffic_limit) VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id, pool_id) DO UPDATE SET mode = excluded.mode, traffic_limit = excluded.traffic_limit;

-- name: UserPoolModes :many
-- The modes of these users' groups, for a traffic batch: a group without a row is in the
-- main traffic.
SELECT user_id, pool_id, mode FROM user_pools
WHERE user_id = ANY(sqlc.arg(user_ids)::bigint[]) AND pool_id = ANY(sqlc.arg(pool_ids)::bigint[]);

-- name: ResetUserPools :exec
-- Pools reset with the main traffic: a new period, a renewal, the admin's reset.
UPDATE user_pools SET used_up = 0, used_down = 0 WHERE user_id = $1;

-- name: PoolUsage :one
-- What a pool still holds that deleting it would destroy (the cascade takes grants and
-- packages with it, and a paid invoice of a package loses the package): traffic users
-- paid for and have left, packages of the catalog, invoices not closed yet.
SELECT
  (SELECT COUNT(*) FROM traffic_grants g
    WHERE g.pool_id = sqlc.arg(pool_id) AND g.remaining > 0 AND (g.expires_at IS NULL OR g.expires_at > CAST(sqlc.arg(now) AS BIGINT))) AS grants,
  (SELECT COUNT(*) FROM traffic_packages k WHERE k.pool_id = sqlc.arg(pool_id) AND k.archived = 0) AS packages,
  (SELECT COUNT(*) FROM payments p JOIN traffic_packages k ON k.id = p.package_id
    WHERE k.pool_id = sqlc.arg(pool_id) AND p.status IN ('pending', 'paid')) AS payments;

-- name: SetTariffUsersPool :exec
-- The users of a tariff follow a group it closed or opened again at once, in the mode the
-- tariff has for it now.
INSERT INTO user_pools (user_id, pool_id, mode, traffic_limit)
SELECT u.id, sqlc.arg(pool_id)::BIGINT, sqlc.arg(mode)::TEXT, sqlc.narg(traffic_limit)::BIGINT
FROM users u WHERE u.tariff_id = sqlc.arg(tariff_id)::BIGINT
ON CONFLICT (user_id, pool_id) DO UPDATE SET mode = excluded.mode, traffic_limit = excluded.traffic_limit;

-- name: ListPackageTariffs :many
-- The tariffs each package is sold to; a package without rows goes to every tariff.
SELECT * FROM package_tariffs ORDER BY package_id, tariff_id;

-- name: ClearPackageTariffs :exec
DELETE FROM package_tariffs WHERE package_id = $1;

-- name: AddPackageTariff :exec
INSERT INTO package_tariffs (package_id, tariff_id) VALUES ($1, $2);

-- name: ListTariffGifts :many
SELECT * FROM tariff_gifts WHERE tariff_id = $1 ORDER BY package_id;

-- name: ListAllTariffGifts :many
SELECT * FROM tariff_gifts ORDER BY tariff_id, package_id;

-- name: ClearTariffGifts :exec
DELETE FROM tariff_gifts WHERE tariff_id = $1;

-- name: AddTariffGift :exec
INSERT INTO tariff_gifts (tariff_id, package_id) VALUES ($1, $2);

-- name: DropPackageGifts :exec
-- A package taken off the catalog is no tariff's gift any more.
DELETE FROM tariff_gifts WHERE package_id = $1;

-- name: SetTariffChosenServers :exec
-- 1: the tariff includes only its chosen nodes and protocols; 0: every server.
UPDATE tariffs SET chosen_servers = $2 WHERE id = $1;

-- name: ListChosenServerTariffs :many
-- The tariffs that include only chosen servers, archived ones too: their subscribers keep
-- the tariff until their term ends.
SELECT id FROM tariffs WHERE chosen_servers = 1 ORDER BY id;

-- name: ListTariffNodes :many
SELECT node_id FROM tariff_nodes WHERE tariff_id = $1 ORDER BY node_id;

-- name: ListTariffInbounds :many
SELECT inbound_id FROM tariff_inbounds WHERE tariff_id = $1 ORDER BY inbound_id;

-- name: ListAllTariffNodes :many
SELECT * FROM tariff_nodes ORDER BY tariff_id, node_id;

-- name: ListAllTariffInbounds :many
SELECT * FROM tariff_inbounds ORDER BY tariff_id, inbound_id;

-- name: ClearTariffNodes :exec
DELETE FROM tariff_nodes WHERE tariff_id = $1;

-- name: ClearTariffInbounds :exec
DELETE FROM tariff_inbounds WHERE tariff_id = $1;

-- name: AddTariffNode :exec
INSERT INTO tariff_nodes (tariff_id, node_id) VALUES ($1, $2) ON CONFLICT DO NOTHING;

-- name: AddTariffInbound :exec
INSERT INTO tariff_inbounds (tariff_id, inbound_id) VALUES ($1, $2) ON CONFLICT DO NOTHING;

-- name: ApplyTariffLimits :exec
-- The users of a tariff take its values of the chosen parts now (ApplyTariffToAll), each
-- only the columns of its part: the traffic per period and its reset, the device limit (with
-- the devices bought on top, an unlimited one staying unlimited) and how it counts, the
-- speed. What they used and their terms stay.
UPDATE users u SET
  traffic_limit  = CASE WHEN sqlc.arg(traffic)::BOOLEAN THEN t.traffic_limit ELSE u.traffic_limit END,
  reset_strategy = CASE WHEN sqlc.arg(traffic)::BOOLEAN THEN t.reset_strategy ELSE u.reset_strategy END,
  device_limit   = CASE WHEN sqlc.arg(devices)::BOOLEAN THEN t.device_limit + u.extra_devices ELSE u.device_limit END,
  device_mode    = CASE WHEN sqlc.arg(devices)::BOOLEAN THEN t.device_mode ELSE u.device_mode END,
  speed_limit    = CASE WHEN sqlc.arg(speed)::BOOLEAN THEN t.speed_limit ELSE u.speed_limit END,
  updated_at     = sqlc.arg(now)::BIGINT
FROM tariffs t
WHERE t.id = sqlc.arg(tariff_id)::BIGINT AND u.tariff_id = t.id;

-- name: ApplyTariffUsersPools :exec
-- The users of a tariff take the modes of its groups (ApplyTariffPools for all of them):
-- every group back in the main traffic first, then the tariff's rows that are not.
WITH reset AS (
  UPDATE user_pools up SET mode = 'main', traffic_limit = NULL
  FROM users u WHERE up.user_id = u.id AND u.tariff_id = sqlc.arg(tariff_id)::BIGINT
  RETURNING up.user_id
)
INSERT INTO user_pools (user_id, pool_id, mode, traffic_limit)
SELECT u.id, tp.pool_id, tp.mode, tp.traffic_limit
FROM users u JOIN tariff_pools tp ON tp.tariff_id = u.tariff_id
WHERE u.tariff_id = sqlc.arg(tariff_id)::BIGINT AND tp.mode <> 'main'
ON CONFLICT (user_id, pool_id) DO UPDATE SET mode = excluded.mode, traffic_limit = excluded.traffic_limit;

-- name: TariffsLeftWithoutServers :many
-- The tariffs that choose their servers and would have none left without node node_id, or
-- without inbound inbound_id (0: none of either): what a deletion must not do to them, since
-- their users would be left with no server and no word of it.
SELECT t.id, t.name FROM tariffs t
WHERE t.chosen_servers = 1
  AND NOT EXISTS (SELECT 1 FROM tariff_nodes tn WHERE tn.tariff_id = t.id AND tn.node_id <> sqlc.arg(node_id)::BIGINT)
  AND NOT EXISTS (SELECT 1 FROM tariff_inbounds ti JOIN inbounds i ON i.id = ti.inbound_id
                  WHERE ti.tariff_id = t.id AND i.id <> sqlc.arg(inbound_id)::BIGINT AND i.node_id <> sqlc.arg(node_id)::BIGINT)
ORDER BY t.id;
