-- name: AddLegacySubToken :execrows
INSERT INTO legacy_sub_tokens (token, user_id, source, not_before) VALUES ($1, $2, $3, $4)
ON CONFLICT (token) DO NOTHING;

-- name: LegacySubTokenUser :one
SELECT sqlc.embed(u), t.not_before FROM legacy_sub_tokens t JOIN users u ON u.id = t.user_id WHERE t.token = $1;

-- name: ListLegacySubTokensOf :many
SELECT token, source FROM legacy_sub_tokens WHERE user_id = $1 ORDER BY token;

-- name: CountLegacySubTokens :one
SELECT count(*) FROM legacy_sub_tokens;

-- name: CountLegacyUsers :one
-- The users with an old link: a PasarGuard user has two keys (name and id), one person.
SELECT count(DISTINCT user_id) FROM legacy_sub_tokens;

-- name: DeleteLegacySubTokensOf :exec
DELETE FROM legacy_sub_tokens WHERE user_id = $1;

-- name: SetImportedUsage :exec
UPDATE users SET used_down = $2, total_down = $3, updated_at = $4 WHERE id = $1;

-- name: TakenUserNames :many
SELECT DISTINCT name FROM users WHERE name = ANY(sqlc.arg(names)::text[]);

-- name: LockUserName :exec
-- Holds a name until the transaction ends: two imports (or retries) at once cannot both
-- find it free.
SELECT pg_advisory_xact_lock(hashtextextended('mikan-user-name:' || sqlc.arg(name)::text, 0));

-- name: UserNameTaken :one
SELECT EXISTS (SELECT 1 FROM users WHERE name = $1);
