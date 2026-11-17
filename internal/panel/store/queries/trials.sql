-- name: TakeTrial :execrows
INSERT INTO trials (tg_id, tariff_id, created_at) VALUES ($1, $2, $3) ON CONFLICT (tg_id) DO NOTHING;

-- name: SetTrialUser :exec
UPDATE trials SET user_id = $1 WHERE tg_id = $2;

-- name: HasTrial :one
SELECT EXISTS (SELECT 1 FROM trials WHERE tg_id = $1);

-- name: GetTrialUser :one
-- The subscription the account's trial gave, NULL once it is deleted; no row: no trial.
SELECT user_id FROM trials WHERE tg_id = $1;

-- name: CountTrials :one
SELECT count(*) FROM trials;

-- name: UserPaidBeyondTrial :one
-- Whether the user paid for anything besides the trial: a payment refunded since included,
-- it was a purchase.
SELECT EXISTS (SELECT 1 FROM payments WHERE user_id = sqlc.arg(user_id)::bigint AND status IN ('paid', 'applied', 'refunded') AND kind <> 'trial');
