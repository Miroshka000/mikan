-- name: AddPaymentAccessTraffic :exec
INSERT INTO payment_access_traffic (user_id, expired_at, bytes)
SELECT id, sqlc.arg(expired_at)::bigint, sqlc.arg(bytes)::bigint
FROM users WHERE id = sqlc.arg(user_id)
ON CONFLICT (user_id, expired_at) DO UPDATE
SET bytes = payment_access_traffic.bytes + EXCLUDED.bytes;

-- name: ListPaymentAccessTraffic :many
SELECT user_id, expired_at, bytes
FROM payment_access_traffic
WHERE expired_at >= sqlc.arg(since)::bigint;

-- name: PaymentAccessTraffic :one
SELECT COALESCE((
    SELECT bytes FROM payment_access_traffic
    WHERE user_id = sqlc.arg(user_id) AND expired_at = sqlc.arg(expired_at)
), 0)::bigint AS used;

-- name: PrunePaymentAccessTraffic :exec
DELETE FROM payment_access_traffic WHERE expired_at < sqlc.arg(before_date)::bigint;
