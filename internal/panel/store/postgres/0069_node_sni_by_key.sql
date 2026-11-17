-- +goose Up
-- The node's SNI router may let REALITY inbounds share a name: it hands each client to the one
-- whose key made its ClientHello (nodeapi.SNIRouter.ByKey).
ALTER TABLE nodes ADD COLUMN sni_by_key BIGINT NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE nodes DROP COLUMN sni_by_key;
