-- name: ListHostsOfInbounds :many
SELECT * FROM inbound_hosts WHERE inbound_id = ANY(sqlc.arg(ids)::bigint[]) ORDER BY inbound_id, position;

-- name: ListInboundHosts :many
SELECT * FROM inbound_hosts WHERE inbound_id = $1 ORDER BY position;

-- name: GetInboundHost :one
SELECT * FROM inbound_hosts WHERE id = $1 AND inbound_id = $2;

-- name: LockInbound :one
-- Holds the inbound while its hosts change: two hosts added at once do not take one position.
SELECT id FROM inbounds WHERE id = $1 FOR UPDATE;

-- name: AddInboundHost :one
-- A new host goes after the others; position 0 stays the default one's.
INSERT INTO inbound_hosts (inbound_id, position, address, port, sni, host, fingerprint, alpn, ip_version, mux, mux_protocol, mux_padding, cdn, name)
VALUES (@inbound_id, (SELECT COALESCE(max(position), 0) + 1 FROM inbound_hosts WHERE inbound_id = @inbound_id), @address, @port, @sni, @host, @fingerprint,
  COALESCE(@alpn::text[], '{}'), @ip_version, @mux, @mux_protocol, @mux_padding, @cdn, @name)
RETURNING *;

-- name: SetDefaultInboundHost :exec
-- The template's five fields only: the host's own (alpn and the rest) stay as they are.
INSERT INTO inbound_hosts (inbound_id, position, address, port, sni, host, fingerprint)
VALUES ($1, 0, $2, $3, $4, $5, $6)
ON CONFLICT (inbound_id) WHERE position = 0 DO UPDATE
SET address = excluded.address, port = excluded.port, sni = excluded.sni, host = excluded.host, fingerprint = excluded.fingerprint;

-- name: UpdateInboundHost :one
UPDATE inbound_hosts SET address = @address, port = @port, sni = @sni, host = @host, fingerprint = @fingerprint,
  alpn = COALESCE(@alpn::text[], '{}'), ip_version = @ip_version, mux = @mux, mux_protocol = @mux_protocol, mux_padding = @mux_padding, cdn = @cdn,
  name = @name
WHERE id = @id AND inbound_id = @inbound_id
RETURNING *;

-- name: SetInboundHostOptions :exec
-- The host's own fields, those the template does not hold: of the default host, next to the
-- template's write of the others.
UPDATE inbound_hosts SET alpn = COALESCE(@alpn::text[], '{}'), ip_version = @ip_version, mux = @mux, mux_protocol = @mux_protocol, mux_padding = @mux_padding, cdn = @cdn,
  name = @name
WHERE id = @id AND inbound_id = @inbound_id;

-- name: DeleteInboundHost :execrows
-- The default host goes only with its inbound.
DELETE FROM inbound_hosts WHERE id = $1 AND inbound_id = $2 AND position > 0;

-- name: CopyInboundHosts :exec
-- A copy of an inbound takes its other hosts; its default one comes from its template.
INSERT INTO inbound_hosts (inbound_id, position, address, port, sni, host, fingerprint, alpn, ip_version, mux, mux_protocol, mux_padding, cdn, name)
SELECT @to_inbound, src.position, src.address, src.port, src.sni, src.host, src.fingerprint,
  src.alpn, src.ip_version, src.mux, src.mux_protocol, src.mux_padding, src.cdn, src.name FROM inbound_hosts src
WHERE src.inbound_id = @from_inbound AND src.position > 0;

-- name: CopyDefaultHostOptions :exec
-- The default host of a copy takes the original's own fields: the template, which gives it
-- the others, does not hold them.
UPDATE inbound_hosts dst SET alpn = src.alpn, ip_version = src.ip_version, mux = src.mux, mux_protocol = src.mux_protocol,
  mux_padding = src.mux_padding, cdn = src.cdn, name = src.name
FROM inbound_hosts src
WHERE src.inbound_id = @from_inbound AND src.position = 0 AND dst.inbound_id = @to_inbound AND dst.position = 0;
