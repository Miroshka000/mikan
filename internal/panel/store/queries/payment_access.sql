-- name: AddPaymentAccessTraffic :exec
-- A counters batch's renewal bytes by user and expiry, each key once, in key order as the
-- other batches take their rows; a user deleted since the node counted is skipped.
INSERT INTO payment_access_traffic (user_id, expired_at, bytes)
SELECT t.user_id, t.expired_at, t.bytes
FROM (SELECT unnest(sqlc.arg(user_ids)::bigint[]) AS user_id, unnest(sqlc.arg(expired_ats)::bigint[]) AS expired_at,
             unnest(sqlc.arg(bytes)::bigint[]) AS bytes) AS t
WHERE EXISTS (SELECT 1 FROM users WHERE users.id = t.user_id)
ORDER BY t.user_id, t.expired_at
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
