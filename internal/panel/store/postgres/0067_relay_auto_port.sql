-- +goose Up
-- A cascade relay's port: the panel moves it by itself when another program holds it or the
-- way between the servers drops it (1), or it stays the one the admin set (0).
ALTER TABLE node_relays ADD COLUMN auto_port BIGINT NOT NULL DEFAULT 1;

-- +goose Down
ALTER TABLE node_relays DROP COLUMN auto_port;
