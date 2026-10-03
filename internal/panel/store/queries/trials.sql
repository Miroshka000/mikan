-- name: TakeTrial :execrows
INSERT INTO trials (tg_id, tariff_id, created_at) VALUES ($1, $2, $3) ON CONFLICT (tg_id) DO NOTHING;

-- name: SetTrialUser :exec
UPDATE trials SET user_id = $1 WHERE tg_id = $2;

-- name: HasTrial :one
SELECT EXISTS (SELECT 1 FROM trials WHERE tg_id = $1);

-- name: CountTrials :one
SELECT count(*) FROM trials;
