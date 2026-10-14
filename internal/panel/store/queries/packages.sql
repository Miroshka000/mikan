-- name: ListTrafficPackages :many
SELECT * FROM traffic_packages WHERE archived = 0 ORDER BY sort, id;

-- name: ListAllTrafficPackages :many
-- Archived ones too: grants and payments keep naming them.
SELECT * FROM traffic_packages ORDER BY id;

-- name: ListTrafficPackagesOnSale :many
SELECT * FROM traffic_packages WHERE archived = 0 AND on_sale = 1 ORDER BY sort, id;

-- name: GetTrafficPackage :one
SELECT * FROM traffic_packages WHERE id = $1;

-- name: CreateTrafficPackage :one
INSERT INTO traffic_packages (name, bytes, pool_id, lifetime, days, price_stars, price_rub, on_sale, sort, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: UpdateTrafficPackage :one
UPDATE traffic_packages
SET name = $1, bytes = $2, pool_id = $3, lifetime = $4, days = $5, price_stars = $6, price_rub = $7, on_sale = $8, sort = $9
WHERE id = $10 AND archived = 0
RETURNING *;

-- name: ArchiveTrafficPackage :execrows
UPDATE traffic_packages SET archived = 1, on_sale = 0 WHERE id = $1 AND archived = 0;

-- name: CreateTrafficGrant :one
INSERT INTO traffic_grants (user_id, pool_id, bytes, remaining, lifetime, expires_at, source, payment_id, package_id, note, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- name: ListUserGrants :many
SELECT * FROM traffic_grants WHERE user_id = $1 ORDER BY created_at DESC, id DESC;

-- name: SumGrantsLeft :many
-- What is left of the active grants per user and target (pool 0: the main traffic).
SELECT user_id, CAST(COALESCE(pool_id, 0) AS BIGINT) AS pool_id, CAST(SUM(remaining) AS BIGINT) AS left_bytes
FROM traffic_grants
WHERE remaining > 0 AND (expires_at IS NULL OR expires_at > CAST(sqlc.arg(now) AS BIGINT))
GROUP BY user_id, COALESCE(pool_id, 0);

-- name: EndPeriodGrants :exec
-- A new traffic period ends the grants that last one period.
UPDATE traffic_grants SET expires_at = CAST(sqlc.arg(now) AS BIGINT)
WHERE user_id = sqlc.arg(user_id) AND lifetime = 'period' AND remaining > 0
  AND (expires_at IS NULL OR expires_at > CAST(sqlc.arg(now) AS BIGINT));

-- name: GetUserPool :one
SELECT * FROM user_pools WHERE user_id = $1 AND pool_id = $2;

-- name: CreatePackagePayment :one
INSERT INTO payments (provider, payload, tg_id, kind, user_id, package_id, tariff_name, amount, currency, status, created_at)
VALUES ($1, $2, $3, 'package', $4, $5, $6, $7, $8, 'pending', $9)
RETURNING *;

-- name: FindOpenPackagePayments :many
SELECT * FROM payments
WHERE tg_id = sqlc.arg(tg_id) AND package_id = sqlc.arg(package_id) AND provider = sqlc.arg(provider) AND kind = 'package' AND user_id = sqlc.arg(user_id)
  AND status = 'pending' AND pay_url <> '' AND created_at > sqlc.arg(since)
ORDER BY id DESC;

-- name: DeleteGrantByPayment :execrows
DELETE FROM traffic_grants WHERE payment_id = $1;

-- name: SumUsersGrantsLeft :many
-- What is left of the active grants of the users in ids, per user and target.
SELECT user_id, CAST(COALESCE(pool_id, 0) AS BIGINT) AS pool_id, CAST(SUM(remaining) AS BIGINT) AS left_bytes
FROM traffic_grants
WHERE user_id = ANY(sqlc.arg(ids)::bigint[]) AND remaining > 0 AND (expires_at IS NULL OR expires_at > CAST(sqlc.arg(now) AS BIGINT))
GROUP BY user_id, COALESCE(pool_id, 0);
