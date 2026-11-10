-- name: GetNodeRelay :one
SELECT * FROM node_relays WHERE node_id = $1;

-- name: ListNodeRelays :many
SELECT * FROM node_relays ORDER BY node_id;

-- name: CreateNodeRelay :one
INSERT INTO node_relays (node_id, port, config, created_at) VALUES ($1, $2, $3, $4)
ON CONFLICT (node_id) DO UPDATE SET node_id = excluded.node_id
RETURNING *;

-- name: SetNodeRelayPort :exec
UPDATE node_relays SET port = $1 WHERE node_id = $2;

-- name: SetNodeRelayRoute :exec
UPDATE node_relays SET outbound = $1, exit_node_id = $2 WHERE node_id = $3;

-- name: SetNodeRelayTarget :exec
UPDATE node_relays SET config = $1, auto_sni = $2 WHERE node_id = $3;

-- name: GetRelayUser :one
SELECT uuid FROM relay_users WHERE exit_node_id = $1 AND src_node_id = $2;

-- name: AddRelayUser :exec
INSERT INTO relay_users (exit_node_id, src_node_id, uuid) VALUES ($1, $2, $3)
ON CONFLICT (exit_node_id, src_node_id) DO NOTHING;

-- name: ListRelayUsers :many
SELECT * FROM relay_users WHERE exit_node_id = $1 ORDER BY src_node_id;

-- name: SetInboundExit :exec
UPDATE inbounds SET exit_node_id = $1, outbound = $2 WHERE id = $3;
