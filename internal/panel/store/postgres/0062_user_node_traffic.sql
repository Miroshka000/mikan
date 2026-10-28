-- +goose Up
-- What each user carried through each node, and when last: the bytes of the users'
-- statistics again (counted in the same transaction as node_traffic_*), grouped by user and
-- node. One row a pair, added to in place by every stored counter batch, so a node's
-- clients and a user's nodes are a read away. Totals since the version that counts them.
CREATE TABLE user_node_traffic (
  user_id   BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  node_id   BIGINT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  up        BIGINT NOT NULL DEFAULT 0,
  down      BIGINT NOT NULL DEFAULT 0,
  last_seen BIGINT NOT NULL, -- the last stored batch with the user's bytes on the node
  PRIMARY KEY (user_id, node_id)
);
CREATE INDEX user_node_traffic_node ON user_node_traffic (node_id, last_seen DESC);

-- +goose Down
DROP TABLE user_node_traffic;
