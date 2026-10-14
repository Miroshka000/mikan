-- +goose Up
-- Each addon's own small store (the panel's API for addons, /kv): addons run without
-- volumes, so what they keep lives here and goes into the panel's backups. version grows
-- with every write and is the ETag a write may be conditioned on; it carries on past an
-- expired value so an old ETag never matches again.
CREATE TABLE addon_kv (
  addon_id   TEXT   NOT NULL,
  key        TEXT   NOT NULL,
  value      BYTEA  NOT NULL,
  version    BIGINT NOT NULL,
  expires_at BIGINT, -- NULL: kept until deleted
  updated_at BIGINT NOT NULL,
  PRIMARY KEY (addon_id, key)
);
CREATE INDEX addon_kv_expires ON addon_kv (expires_at) WHERE expires_at IS NOT NULL;

-- +goose Down
DROP TABLE addon_kv;
