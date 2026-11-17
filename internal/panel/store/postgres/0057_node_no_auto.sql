-- +goose Up
-- The node's servers stay out of the subscriptions' automatic choice (1): a server for
-- whitelist-only networks the apps must not pick on their own. They stay in the main group
-- and in the node's country group, to pick by hand.
ALTER TABLE nodes ADD COLUMN no_auto BIGINT NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE nodes DROP COLUMN no_auto;
