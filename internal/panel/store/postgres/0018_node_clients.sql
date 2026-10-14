-- +goose Up
-- Who went through which node: how much each user carried over each node and when it was
-- last seen there. The per-node tables say what a node carried; the per-user ones say how
-- much a user used; this says by whom and where. Written on the counters' transaction
-- (alongside node_traffic_*), so a node's rows add up to what it carried and a user's rows
-- to what the user used.
CREATE TABLE node_clients (
  user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  node_id    BIGINT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  up         BIGINT NOT NULL DEFAULT 0,
  down       BIGINT NOT NULL DEFAULT 0,
  first_seen BIGINT NOT NULL DEFAULT 0,
  last_seen  BIGINT NOT NULL DEFAULT 0,
  PRIMARY KEY (user_id, node_id)
);
-- The card of a node reads its clients by node; the primary key serves the user's card.
CREATE INDEX node_clients_node ON node_clients (node_id);
CREATE INDEX node_clients_last_seen ON node_clients (last_seen);

-- +goose Down
DROP TABLE node_clients;
