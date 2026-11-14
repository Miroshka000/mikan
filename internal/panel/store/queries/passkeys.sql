-- name: ListAdminPasskeys :many
SELECT * FROM admin_passkeys WHERE admin_id = $1 ORDER BY id;

-- name: CreateAdminPasskey :one
INSERT INTO admin_passkeys (admin_id, credential_id, name, credential, created_at)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: UseAdminPasskey :exec
-- After a sign-in: the record with the new signature counter, and when.
UPDATE admin_passkeys SET credential = $1, last_used_at = $2 WHERE id = $3;

-- name: DeleteAdminPasskey :execrows
DELETE FROM admin_passkeys WHERE id = $1 AND admin_id = $2;

-- name: DeleteAdminPasskeysOf :execrows
-- With a new password (mikan reset-password, a change that revokes the keys): a passkey
-- added from a hijacked session would outlive the reaction to it.
DELETE FROM admin_passkeys WHERE admin_id = $1;
