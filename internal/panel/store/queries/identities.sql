-- name: ListAdminIdentities :many
SELECT * FROM admin_identities WHERE admin_id = $1 ORDER BY provider;

-- name: GetAdminIdentity :one
-- The admin an account opens.
SELECT * FROM admin_identities WHERE provider = $1 AND subject = $2;

-- name: CreateAdminIdentity :one
INSERT INTO admin_identities (admin_id, provider, subject, name, created_at)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: UseAdminIdentity :exec
-- After a sign-in: when, and the account's name as it is now.
UPDATE admin_identities SET last_used_at = $1, name = $2 WHERE id = $3;

-- name: DeleteAdminIdentity :execrows
DELETE FROM admin_identities WHERE id = $1 AND admin_id = $2;

-- name: DeleteAdminIdentitiesOf :execrows
-- With a new password, as the passkeys: an account linked from a hijacked session would
-- outlive the reaction to it.
DELETE FROM admin_identities WHERE admin_id = $1;

-- name: CountAdminIdentities :one
-- Whether anyone signs in with the provider: the login page offers it only then.
SELECT COUNT(*) FROM admin_identities WHERE provider = $1;
