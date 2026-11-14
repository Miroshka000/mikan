-- +goose Up
-- A cascade relay's REALITY site: the panel keeps it in line with its node by itself (1, the
-- site of the node's protocols or one next to the server), or it stays the one the admin set (0).
ALTER TABLE node_relays ADD COLUMN auto_sni BIGINT NOT NULL DEFAULT 1;

-- +goose Down
ALTER TABLE node_relays DROP COLUMN auto_sni;
