-- name: ClaimFreePlan :one
-- The account's row, made when it has none and locked either way: whoever takes the free
-- plan at the same time waits here, then finds the subscription the first one made.
INSERT INTO free_plans (tg_id, created_at) VALUES ($1, $2)
ON CONFLICT (tg_id) DO UPDATE SET tg_id = EXCLUDED.tg_id
RETURNING user_id;

-- name: SetFreePlanUser :exec
UPDATE free_plans SET user_id = $1, tariff_id = $2, created_at = $3, taken = taken + 1 WHERE tg_id = $4;

-- name: GetFreePlanUser :one
SELECT user_id FROM free_plans WHERE tg_id = $1;

-- name: CountFreePlans :one
SELECT CAST(COALESCE(sum(taken), 0) AS BIGINT) FROM free_plans;
