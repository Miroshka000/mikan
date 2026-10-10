-- name: ListNodes :many
SELECT * FROM nodes ORDER BY sort, id;

-- name: GetNode :one
SELECT * FROM nodes WHERE id = $1;

-- A new node goes last in the subscription.
-- name: CreateNode :one
INSERT INTO nodes (name, address, public_host, domain, cert_sha256, enabled, created_at, updated_at, sort)
VALUES ($1, $2, $3, $4, $5, 1, $6, $7, (SELECT COALESCE(MAX(sort), 0) + 1 FROM nodes))
RETURNING *;

-- name: SetNodeSort :exec
UPDATE nodes SET sort = $1 WHERE id = $2;

-- name: UpdateNode :one
UPDATE nodes SET name = $1, address = $2, public_host = $3, domain = $4, public_name = $5, enabled = $6, updated_at = $7 WHERE id = $8 RETURNING *;

-- name: SetNodeShaping :exec
UPDATE nodes SET fair_share = $1, channel_mbps = $2, updated_at = $3 WHERE id = $4;

-- name: SetNodeCert :exec
UPDATE nodes SET cert_sha256 = $1, updated_at = $2 WHERE id = $3;

-- name: DeleteNode :exec
DELETE FROM nodes WHERE id = $1 AND id != 1;
