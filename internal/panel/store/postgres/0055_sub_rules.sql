-- +goose Up
-- The response rules of the subscription (internal/sub/apps): what the panel answers a
-- request by its headers, in the order the admin put them; the first that matches decides.
-- The conditions and the action are JSON, an apps.Group and an apps.Action.
CREATE TABLE sub_rules (
  position BIGINT PRIMARY KEY,
  name     TEXT NOT NULL,
  enabled  BOOLEAN NOT NULL DEFAULT true,
  cond     TEXT NOT NULL,
  action   TEXT NOT NULL
);

-- The known apps (apps.Catalog ids) the admin turned away: they get a stub in place of the
-- servers and the subscription page does not list them.
CREATE TABLE sub_blocked_apps (
  app TEXT PRIMARY KEY
);

-- The request log: a row for each fetch of a subscription, kept for sub_log_days. It
-- records what asked and what it came to, with no token and no key.
CREATE TABLE sub_log (
  id      BIGSERIAL PRIMARY KEY,
  user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  at      BIGINT NOT NULL, -- unix seconds
  ip      TEXT NOT NULL,
  agent   TEXT NOT NULL,
  app     TEXT NOT NULL DEFAULT '',
  rule    TEXT NOT NULL DEFAULT '',
  format  TEXT NOT NULL DEFAULT '',
  outcome TEXT NOT NULL CHECK (outcome IN ('served', 'stub', 'blocked', 'dropped'))
);
CREATE INDEX sub_log_user ON sub_log (user_id, at DESC);
CREATE INDEX sub_log_at ON sub_log (at);

-- +goose Down
DROP TABLE sub_log;
DROP TABLE sub_blocked_apps;
DROP TABLE sub_rules;
