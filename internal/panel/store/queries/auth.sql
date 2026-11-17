-- name: GetSetting :one
SELECT value FROM settings WHERE key = $1;

-- name: SetSetting :exec
INSERT INTO settings (key, value) VALUES ($1, $2)
ON CONFLICT (key) DO UPDATE SET value = excluded.value;

-- name: CountAdmins :one
SELECT count(*) FROM admins;

-- name: ListAdmins :many
SELECT * FROM admins ORDER BY id;

-- name: GetAdmin :one
SELECT * FROM admins WHERE id = $1;

-- name: GetAdminByUsername :one
SELECT * FROM admins WHERE username = $1;

-- name: CreateAdmin :one
INSERT INTO admins (username, password_hash, created_at) VALUES ($1, $2, $3)
RETURNING *;

-- name: SetAdminPassword :exec
UPDATE admins SET password_hash = $1 WHERE id = $2;

-- name: SetAdminTOTP :exec
UPDATE admins SET totp_secret = $1, recovery_codes = $2 WHERE id = $3;

-- name: SpendAdminRecoveryCodes :execrows
-- Takes a recovery code out of the list it was found in: 0 rows means another login spent
-- a code from the same list first, and this one is refused.
UPDATE admins SET recovery_codes = sqlc.arg(rest) WHERE id = sqlc.arg(id) AND recovery_codes = sqlc.arg(was);

-- name: SetAdminLastLogin :exec
UPDATE admins SET last_login_at = $1 WHERE id = $2;

-- name: CreateSession :exec
-- support_grant_id is set for a support operator's session (internal/panel/support), NULL for the admin's own.
INSERT INTO sessions (id_hash, admin_id, csrf_token, created_at, last_seen_at, expires_at, ip, user_agent, support_grant_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: GetSession :one
SELECT * FROM sessions WHERE id_hash = $1;

-- name: TouchSession :exec
UPDATE sessions SET last_seen_at = $1 WHERE id_hash = $2;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE id_hash = $1;

-- name: DeleteAdminSessions :exec
DELETE FROM sessions WHERE admin_id = $1;

-- name: DeleteAdminSessionsExcept :exec
DELETE FROM sessions WHERE admin_id = $1 AND id_hash <> $2;

-- name: ListAdminSessions :many
-- The admin's own sessions: an operator's belong to its grant, which the admin ends there.
SELECT * FROM sessions WHERE admin_id = $1 AND support_grant_id IS NULL ORDER BY last_seen_at DESC;

-- name: DeleteStaleSessions :exec
DELETE FROM sessions WHERE expires_at < sqlc.arg(now) OR last_seen_at < sqlc.arg(idle_before);

-- name: InsertAudit :exec
INSERT INTO audit_log (ts, admin_id, action, target_type, target_id, ip, details)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: ListAudit :many
SELECT * FROM audit_log WHERE id < sqlc.arg(before_id) ORDER BY id DESC LIMIT CAST(sqlc.arg(lim) AS BIGINT);

-- name: PruneAudit :execrows
DELETE FROM audit_log WHERE ts < $1;

-- name: GetOrCreateSetting :one
-- Keep the winner when several panel processes initialize the same internal secret.
INSERT INTO settings (key, value) VALUES ($1, $2)
ON CONFLICT (key) DO UPDATE SET key = excluded.key
RETURNING value;
