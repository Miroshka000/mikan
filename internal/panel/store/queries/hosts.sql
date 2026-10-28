-- name: ListAllInboundHosts :many
SELECT * FROM inbound_hosts ORDER BY inbound_id, position;

-- name: ListInboundHosts :many
SELECT * FROM inbound_hosts WHERE inbound_id = $1 ORDER BY position;

-- name: GetInboundHost :one
SELECT * FROM inbound_hosts WHERE id = $1 AND inbound_id = $2;

-- name: LockInbound :one
-- Holds the inbound while its hosts change: two hosts added at once do not take one position.
SELECT id FROM inbounds WHERE id = $1 FOR UPDATE;

-- name: AddInboundHost :one
-- A new host goes after the others; position 0 stays the default one's.
INSERT INTO inbound_hosts (inbound_id, position, address, port, sni, host, fingerprint)
VALUES (@inbound_id, (SELECT COALESCE(max(position), 0) + 1 FROM inbound_hosts WHERE inbound_id = @inbound_id), @address, @port, @sni, @host, @fingerprint)
RETURNING *;

-- name: SetDefaultInboundHost :exec
INSERT INTO inbound_hosts (inbound_id, position, address, port, sni, host, fingerprint)
VALUES ($1, 0, $2, $3, $4, $5, $6)
ON CONFLICT (inbound_id, position) DO UPDATE
SET address = excluded.address, port = excluded.port, sni = excluded.sni, host = excluded.host, fingerprint = excluded.fingerprint;

-- name: UpdateInboundHost :one
UPDATE inbound_hosts SET address = $3, port = $4, sni = $5, host = $6, fingerprint = $7
WHERE id = $1 AND inbound_id = $2
RETURNING *;

-- name: DeleteInboundHost :execrows
-- The default host goes only with its inbound.
DELETE FROM inbound_hosts WHERE id = $1 AND inbound_id = $2 AND position > 0;

-- name: CopyInboundHosts :exec
-- A copy of an inbound takes its other hosts; its default one comes from its template.
INSERT INTO inbound_hosts (inbound_id, position, address, port, sni, host, fingerprint)
SELECT @to_inbound, src.position, src.address, src.port, src.sni, src.host, src.fingerprint FROM inbound_hosts src
WHERE src.inbound_id = @from_inbound AND src.position > 0;
