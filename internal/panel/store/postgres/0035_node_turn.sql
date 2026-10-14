-- +goose Up
-- The way in through VK calls ("WL") on a node: the panel runs the vk-turn-proxy server
-- there, and the server hands what it unwraps to one inbound of the same node. Clients
-- reach it over the TURN servers of VK calls and speak to that inbound as if it stood on
-- their own machine; the subscription lists it as a copy of that inbound at
-- client_host:client_port, under the name the admin gives it.
--
-- vk_links are the VK call links the clients are told to join, one per line (a call
-- carries a limited rate, so a panel with many users spreads them over several calls);
-- an empty list leaves the entry without a link.
CREATE TABLE node_turn (
  node_id     BIGINT PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
  enabled     BIGINT NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
  inbound_id  BIGINT NOT NULL REFERENCES "inbounds"(id) ON DELETE CASCADE,
  listen_port TEXT NOT NULL DEFAULT '56000',
  client_host TEXT NOT NULL DEFAULT '127.0.0.1',
  client_port TEXT NOT NULL DEFAULT '9000',
  streams     BIGINT NOT NULL DEFAULT 10,
  udp         BIGINT NOT NULL DEFAULT 1 CHECK (udp IN (0,1)),
  title       TEXT NOT NULL DEFAULT '',
  vk_links    TEXT NOT NULL DEFAULT '',
  binary_path TEXT NOT NULL DEFAULT '',
  created_at  BIGINT NOT NULL,
  updated_at  BIGINT NOT NULL
);

-- +goose Down
DROP TABLE node_turn;
