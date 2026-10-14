-- +goose Up
-- The feed of events for addons (internal/panel/events): written in the same transaction as
-- the change it tells of, under an advisory lock so ids commit in order and a reader that has
-- seen an id never finds a smaller one later. Kept a week, read or not. dedupe_key makes a
-- second emit of the same thing a no-op.
CREATE TABLE addon_events (
  id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  type       TEXT   NOT NULL,
  user_id    BIGINT,
  tg_id      BIGINT,
  data       TEXT   NOT NULL, -- JSON
  dedupe_key TEXT   UNIQUE,
  created_at BIGINT NOT NULL
);
CREATE INDEX addon_events_created ON addon_events (created_at);

-- How far each addon has read the feed.
CREATE TABLE addon_cursors (
  addon_id   TEXT   PRIMARY KEY,
  last_id    BIGINT NOT NULL,
  updated_at BIGINT NOT NULL
);

-- +goose Down
DROP TABLE addon_cursors;
DROP TABLE addon_events;
