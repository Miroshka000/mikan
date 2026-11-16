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
-- A new host goes after the others; position 0 stays the default one's. In the subscriptions
-- it takes its inbound's last place, and its position puts it after that host.
INSERT INTO inbound_hosts (inbound_id, position, sort, address, port, sni, host, fingerprint, alpn, ip_version, mux, mux_protocol, mux_padding, cdn, name, addresses, snis, states, trial)
VALUES (@inbound_id, (SELECT COALESCE(max(position), 0) + 1 FROM inbound_hosts WHERE inbound_id = @inbound_id),
  COALESCE(
    (SELECT max(sort) FROM inbound_hosts WHERE inbound_id = @inbound_id),
    (SELECT max(h.sort) FROM inbound_hosts h JOIN inbounds i ON i.id = h.inbound_id WHERE i.node_id = (SELECT node_id FROM inbounds WHERE id = @inbound_id)),
    (SELECT COALESCE(max(sort), 0) + 1 FROM inbound_hosts)),
  @address, @port, @sni, @host, @fingerprint,
  COALESCE(@alpn::text[], '{}'), @ip_version, @mux, @mux_protocol, @mux_padding, @cdn, @name,
  COALESCE(@addresses::text[], '{}'), COALESCE(@snis::text[], '{}'), COALESCE(@states::text[], '{}'), @trial)
RETURNING *;

-- name: SetDefaultInboundHost :exec
-- The template's five fields only: the host's own (alpn and the rest) stay as they are, and
-- its place too. A new inbound's default host takes the last place of its node's servers (the
-- inbound's id puts it after them), the first one of a node without servers the place after all.
INSERT INTO inbound_hosts (inbound_id, position, sort, address, port, sni, host, fingerprint)
VALUES ($1, 0,
  COALESCE(
    (SELECT max(sort) FROM inbound_hosts WHERE inbound_id = $1),
    (SELECT max(h.sort) FROM inbound_hosts h JOIN inbounds i ON i.id = h.inbound_id WHERE i.node_id = (SELECT node_id FROM inbounds WHERE id = $1)),
    (SELECT COALESCE(max(sort), 0) + 1 FROM inbound_hosts)),
  $2, $3, $4, $5, $6)
ON CONFLICT (inbound_id) WHERE position = 0 DO UPDATE
SET address = excluded.address, port = excluded.port, sni = excluded.sni, host = excluded.host, fingerprint = excluded.fingerprint;

-- name: UpdateInboundHost :one
UPDATE inbound_hosts SET address = @address, port = @port, sni = @sni, host = @host, fingerprint = @fingerprint,
  alpn = COALESCE(@alpn::text[], '{}'), ip_version = @ip_version, mux = @mux, mux_protocol = @mux_protocol, mux_padding = @mux_padding, cdn = @cdn,
  name = @name, addresses = COALESCE(@addresses::text[], '{}'), snis = COALESCE(@snis::text[], '{}'),
  states = COALESCE(@states::text[], '{}'), trial = @trial
WHERE id = @id AND inbound_id = @inbound_id
RETURNING *;

-- name: SetInboundHostOptions :exec
-- The host's own fields, those the template does not hold: of the default host, next to the
-- template's write of the others.
UPDATE inbound_hosts SET alpn = COALESCE(@alpn::text[], '{}'), ip_version = @ip_version, mux = @mux, mux_protocol = @mux_protocol, mux_padding = @mux_padding, cdn = @cdn,
  name = @name, addresses = COALESCE(@addresses::text[], '{}'), snis = COALESCE(@snis::text[], '{}'),
  states = COALESCE(@states::text[], '{}'), trial = @trial
WHERE id = @id AND inbound_id = @inbound_id;

-- name: DeleteInboundHost :execrows
-- The default host goes only with its inbound.
DELETE FROM inbound_hosts WHERE id = $1 AND inbound_id = $2 AND position > 0;

-- name: CopyInboundHosts :exec
-- A copy of an inbound takes its other hosts; its default one comes from its template. They
-- take the place of the copy's default host, after which their positions put them.
INSERT INTO inbound_hosts (inbound_id, position, sort, address, port, sni, host, fingerprint, alpn, ip_version, mux, mux_protocol, mux_padding, cdn, name, addresses, snis, states, trial)
SELECT @to_inbound, src.position,
  COALESCE(
    (SELECT max(sort) FROM inbound_hosts WHERE inbound_id = @to_inbound),
    (SELECT max(h.sort) FROM inbound_hosts h JOIN inbounds i ON i.id = h.inbound_id WHERE i.node_id = (SELECT node_id FROM inbounds WHERE id = @to_inbound)),
    (SELECT COALESCE(max(sort), 0) + 1 FROM inbound_hosts)),
  src.address, src.port, src.sni, src.host, src.fingerprint,
  src.alpn, src.ip_version, src.mux, src.mux_protocol, src.mux_padding, src.cdn, src.name, src.addresses, src.snis, src.states, src.trial FROM inbound_hosts src
WHERE src.inbound_id = @from_inbound AND src.position > 0;

-- name: CopyDefaultHostOptions :exec
-- The default host of a copy takes the original's own fields: the template, which gives it
-- the others, does not hold them.
UPDATE inbound_hosts dst SET alpn = src.alpn, ip_version = src.ip_version, mux = src.mux, mux_protocol = src.mux_protocol,
  mux_padding = src.mux_padding, cdn = src.cdn, name = src.name,
  addresses = src.addresses, snis = src.snis, states = src.states, trial = src.trial
FROM inbound_hosts src
WHERE src.inbound_id = @from_inbound AND src.position = 0 AND dst.inbound_id = @to_inbound AND dst.position = 0;

-- name: ListHostsInOrder :many
-- Every host of the panel in the subscriptions' order: by place, and a tie as the
-- subscriptions gave them before places, node by node in the nodes' order, a node's inbounds
-- by id, an inbound's hosts by position (build.Proxies keeps that order in a tie too).
SELECT sqlc.embed(h), i.node_id FROM inbound_hosts h
JOIN inbounds i ON i.id = h.inbound_id
JOIN nodes n ON n.id = i.node_id
ORDER BY h.sort, n.sort, n.id, i.id, h.position;

-- name: SetHostSort :exec
UPDATE inbound_hosts SET sort = $1 WHERE id = $2;

-- name: LockSubOrder :exec
-- Taken by a change of the subscription's order (of the servers, of the nodes) and by a new
-- inbound or host: a new one takes its place from the order as it is once a change of it
-- commits, and a change of it sees every host there is.
SELECT pg_advisory_xact_lock(hashtextextended('mikan-sub-order', 0));
