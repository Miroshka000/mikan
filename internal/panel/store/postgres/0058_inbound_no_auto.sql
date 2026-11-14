-- +goose Up
-- The inbound stays out of the subscriptions' automatic choice (1), as a node can
-- (nodes.no_auto): one protocol of a node the apps must not pick on their own.
ALTER TABLE inbounds ADD COLUMN no_auto BIGINT NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE inbounds DROP COLUMN no_auto;
