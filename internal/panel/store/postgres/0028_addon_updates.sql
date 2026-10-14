-- +goose Up
-- The Telegram updates the gateway keeps for the bot addon (the Bot API proxy): written as
-- they come, read by the addon's getUpdates and dropped when it acknowledges them with its
-- offset, as Telegram does. Payments never get here: the panel takes them itself.
CREATE TABLE addon_updates (
  update_id  BIGINT PRIMARY KEY,
  body       TEXT   NOT NULL, -- the update's JSON as Telegram sent it
  created_at BIGINT NOT NULL
);

-- +goose Down
DROP TABLE addon_updates;
