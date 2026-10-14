-- name: GetAddonGrant :one
SELECT * FROM addon_grants WHERE addon_id = $1;

-- name: UpsertAddonGrant :exec
INSERT INTO addon_grants (addon_id, version, permissions, approved_by, approved_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (addon_id) DO UPDATE SET version = EXCLUDED.version, permissions = EXCLUDED.permissions,
  approved_by = EXCLUDED.approved_by, approved_at = EXCLUDED.approved_at;

-- name: DeleteAddonGrant :exec
DELETE FROM addon_grants WHERE addon_id = $1;

-- name: GetAddonValue :one
SELECT * FROM addon_kv WHERE addon_id = $1 AND key = $2 AND (expires_at IS NULL OR expires_at > sqlc.arg(now)::BIGINT);

-- name: LockAddonValue :one
SELECT * FROM addon_kv WHERE addon_id = $1 AND key = $2 FOR UPDATE;

-- name: PutAddonValue :exec
INSERT INTO addon_kv (addon_id, key, value, version, expires_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (addon_id, key) DO UPDATE SET value = EXCLUDED.value, version = EXCLUDED.version,
  expires_at = EXCLUDED.expires_at, updated_at = EXCLUDED.updated_at;

-- name: DeleteAddonValue :execrows
DELETE FROM addon_kv WHERE addon_id = $1 AND key = $2;

-- name: AddonKVSize :one
SELECT COALESCE(sum(octet_length(value) + octet_length(key)), 0)::BIGINT AS size
FROM addon_kv WHERE addon_id = $1 AND key <> $2 AND (expires_at IS NULL OR expires_at > sqlc.arg(now)::BIGINT);

-- name: ListAddonKeys :many
SELECT key, version, expires_at FROM addon_kv
WHERE addon_id = $1 AND starts_with(key, sqlc.arg(prefix)::TEXT) AND key > sqlc.arg(after)::TEXT
  AND (expires_at IS NULL OR expires_at > sqlc.arg(now)::BIGINT)
ORDER BY key LIMIT sqlc.arg(lim)::INT;

-- name: PruneAddonKV :execrows
DELETE FROM addon_kv WHERE expires_at IS NOT NULL AND expires_at <= sqlc.arg(now)::BIGINT;

-- name: LockAddonEvents :exec
SELECT pg_advisory_xact_lock(sqlc.arg(key)::BIGINT);

-- name: AddAddonEvent :exec
INSERT INTO addon_events (type, user_id, tg_id, data, dedupe_key, created_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (dedupe_key) DO NOTHING;

-- name: ListAddonEvents :many
SELECT * FROM addon_events
WHERE id > sqlc.arg(after)::BIGINT
  AND (COALESCE(cardinality(sqlc.arg(types)::TEXT[]), 0) = 0 OR type = ANY(sqlc.arg(types)::TEXT[]))
  AND (sqlc.arg(bot_too)::BOOLEAN OR type NOT LIKE 'bot.%')
ORDER BY id LIMIT sqlc.arg(lim)::INT;

-- name: GetAddonCursor :one
SELECT COALESCE((SELECT last_id FROM addon_cursors WHERE addon_id = $1), 0)::BIGINT AS last_id;

-- name: AckAddonEvents :exec
INSERT INTO addon_cursors (addon_id, last_id, updated_at)
VALUES ($1, LEAST(sqlc.arg(last_id)::BIGINT, (SELECT COALESCE(max(id), 0) FROM addon_events)), $2)
ON CONFLICT (addon_id) DO UPDATE SET last_id = GREATEST(addon_cursors.last_id, EXCLUDED.last_id), updated_at = EXCLUDED.updated_at;

-- name: PruneAddonEvents :execrows
DELETE FROM addon_events WHERE created_at < sqlc.arg(before)::BIGINT;

-- name: AddAddonUpdate :exec
INSERT INTO addon_updates (update_id, body, created_at) VALUES ($1, $2, $3)
ON CONFLICT (update_id) DO NOTHING;

-- name: CountAddonUpdates :one
SELECT count(*) FROM addon_updates;

-- name: ListAddonUpdates :many
SELECT update_id, body FROM addon_updates WHERE update_id >= sqlc.arg(from_id)::BIGINT
ORDER BY update_id LIMIT sqlc.arg(lim)::INT;

-- name: AckAddonUpdates :exec
DELETE FROM addon_updates WHERE update_id < sqlc.arg(below)::BIGINT;

-- name: PruneAddonUpdates :execrows
DELETE FROM addon_updates WHERE created_at < sqlc.arg(before)::BIGINT;

-- name: DeleteAddonData :exec
DELETE FROM addon_kv WHERE addon_id = $1;

-- name: DeleteAddonCursor :exec
DELETE FROM addon_cursors WHERE addon_id = $1;

-- name: ListAddonSources :many
SELECT * FROM addon_sources ORDER BY id;

-- name: GetAddonSource :one
SELECT * FROM addon_sources WHERE id = $1;

-- name: AddAddonSource :one
INSERT INTO addon_sources (url, key, title, added_by, added_at, fetched_at, error)
VALUES ($1, $2, $3, $4, $5, $6, '')
RETURNING *;

-- name: SetAddonSourceFetch :exec
UPDATE addon_sources SET fetched_at = $2, error = $3 WHERE id = $1;

-- name: SetAddonSourceKey :exec
UPDATE addon_sources SET key = $2 WHERE id = $1;

-- name: DeleteAddonSource :exec
DELETE FROM addon_sources WHERE id = $1;

-- name: CountAddonSources :one
SELECT count(*) FROM addon_sources;

-- name: ListAddonOrigins :many
SELECT * FROM addon_origins ORDER BY addon_id;

-- name: UpsertAddonOrigin :exec
INSERT INTO addon_origins (addon_id, source_id) VALUES ($1, $2)
ON CONFLICT (addon_id) DO UPDATE SET source_id = EXCLUDED.source_id;

-- name: DeleteAddonOrigin :exec
DELETE FROM addon_origins WHERE addon_id = $1;

-- name: DeleteAddonOriginsOf :exec
DELETE FROM addon_origins WHERE source_id = $1;

-- name: ListAddonClaims :many
SELECT * FROM addon_id_claims ORDER BY addon_id;

-- name: ClaimAddonID :exec
INSERT INTO addon_id_claims (addon_id, source_id) VALUES ($1, $2)
ON CONFLICT (addon_id) DO NOTHING;

-- name: ReleaseAddonClaim :exec
DELETE FROM addon_id_claims WHERE addon_id = $1 AND source_id = $2;

-- name: LockAddonSources :exec
SELECT pg_advisory_xact_lock(hashtextextended('mikan-addon-sources', 0));
-- name: ClaimAddonGrantKey :execrows
-- The key of a grant, taken before the grant: 0 rows is a key already used (a repeat). A
-- concurrent claim of the same key waits for the first one's commit.
INSERT INTO addon_grant_keys (addon_id, key, user_id, result, created_at) VALUES ($1, $2, $3, '', $4)
ON CONFLICT (addon_id, key) DO NOTHING;

-- name: SetAddonGrantResult :exec
UPDATE addon_grant_keys SET result = $3 WHERE addon_id = $1 AND key = $2;

-- name: GetAddonGrantKey :one
SELECT * FROM addon_grant_keys WHERE addon_id = $1 AND key = $2;

-- name: DeleteAddonGrantKeys :exec
DELETE FROM addon_grant_keys WHERE addon_id = $1;
