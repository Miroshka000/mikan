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

-- name: ListTariffPools :many
SELECT * FROM tariff_pools WHERE tariff_id = $1 ORDER BY pool_id;

-- name: ListAllTariffPools :many
SELECT * FROM tariff_pools ORDER BY tariff_id, pool_id;

-- name: ClearTariffPools :exec
DELETE FROM tariff_pools WHERE tariff_id = $1;

-- name: AddTariffPool :exec
INSERT INTO tariff_pools (tariff_id, pool_id, traffic_limit, excluded) VALUES ($1, $2, $3, $4);

-- name: ListUserIDsOfTariff :many
-- The users a tariff was given to, for bringing them up to the tariff's pool limits.
SELECT id FROM users WHERE tariff_id = $1 ORDER BY id;

-- name: ListUserPools :many
SELECT * FROM user_pools WHERE user_id = $1 ORDER BY pool_id;

-- name: ListAllUserPools :many
SELECT * FROM user_pools ORDER BY user_id, pool_id;

-- name: ClearUserPoolLimits :exec
UPDATE user_pools SET traffic_limit = NULL, excluded = false WHERE user_id = $1;

-- name: SetUserPoolLimit :exec
-- An excluded pool has no limit: nothing of it is sold or shown, the user cannot use it.
INSERT INTO user_pools (user_id, pool_id, traffic_limit, excluded) VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id, pool_id) DO UPDATE SET traffic_limit = excluded.traffic_limit, excluded = excluded.excluded;

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
-- The users of a tariff follow a pool it closed or opened again at once; the limit of a
-- pool opened again is the tariff's (NULL: none).
INSERT INTO user_pools (user_id, pool_id, traffic_limit, excluded)
SELECT u.id, sqlc.arg(pool_id)::BIGINT, sqlc.narg(traffic_limit)::BIGINT, sqlc.arg(excluded)::BOOLEAN
FROM users u WHERE u.tariff_id = sqlc.arg(tariff_id)::BIGINT
ON CONFLICT (user_id, pool_id) DO UPDATE SET traffic_limit = excluded.traffic_limit, excluded = excluded.excluded;
