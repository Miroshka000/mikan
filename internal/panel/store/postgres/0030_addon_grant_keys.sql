-- +goose Up
-- One grant per (addon, key), ever: an addon with users:grant (internal/panel/addons/panelapi)
-- names each days/traffic grant by a key of its own, and a repeat of the key (a retried
-- request, a replayed event) answers the first result and changes nothing. Written in the
-- transaction of the grant itself, so a key never exists without its grant. result is the
-- answer as JSON, text like the events' data.
CREATE TABLE addon_grant_keys (
  addon_id   TEXT   NOT NULL,
  key        TEXT   NOT NULL,
  user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  result     TEXT   NOT NULL,
  created_at BIGINT NOT NULL,
  PRIMARY KEY (addon_id, key)
);
CREATE INDEX addon_grant_keys_user ON addon_grant_keys (user_id);

-- +goose Down
DROP TABLE addon_grant_keys;
