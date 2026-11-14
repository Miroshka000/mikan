-- +goose Up
-- The core's egress guard keeps a node's users off the server's own addresses: these are the
-- ports the admin leaves open on them, besides the ones the panel keeps open by itself.
ALTER TABLE nodes ADD COLUMN own_ports BIGINT[] NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE nodes DROP COLUMN own_ports;
