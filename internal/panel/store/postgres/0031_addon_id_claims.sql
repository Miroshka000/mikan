-- +goose Up
-- The first source to list an addon id keeps it (internal/panel/addons, sources.go): a later
-- source, or the same one after the panel restarted with nothing in memory, never takes it
-- over. A claim goes with its source, and is let go when the source stops listing the id.
CREATE TABLE addon_id_claims (
  addon_id  TEXT   PRIMARY KEY,
  source_id BIGINT NOT NULL REFERENCES addon_sources(id) ON DELETE CASCADE
);
CREATE INDEX addon_id_claims_source ON addon_id_claims (source_id);

-- +goose Down
DROP TABLE addon_id_claims;
