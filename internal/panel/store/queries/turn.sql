-- name: GetNodeTurn :one
SELECT * FROM node_turn WHERE node_id = $1;

-- name: SaveNodeTurn :exec
INSERT INTO node_turn (node_id, enabled, inbound_id, listen_port, client_host, client_port, streams, udp, title,
  vk_links, binary_path, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
ON CONFLICT (node_id) DO UPDATE SET enabled = excluded.enabled, inbound_id = excluded.inbound_id,
  listen_port = excluded.listen_port, client_host = excluded.client_host, client_port = excluded.client_port,
  streams = excluded.streams, udp = excluded.udp, title = excluded.title, vk_links = excluded.vk_links,
  binary_path = excluded.binary_path, updated_at = excluded.updated_at;

-- name: DeleteNodeTurn :execrows
DELETE FROM node_turn WHERE node_id = $1;

-- name: ListNodeTurns :many
SELECT t.* FROM node_turn t JOIN nodes n ON n.id = t.node_id
WHERE t.enabled = 1 AND n.enabled = 1;
