-- +goose Up
-- What the admin approved for an addon of protocol 2 (internal/panel/addons): the
-- permissions of the version they were shown. The panel's API for addons lets an addon do
-- only what its grant holds, and an update asking for more waits for a new approval. The
-- tokens are not here: the host's state.json is their source of truth.
CREATE TABLE addon_grants (
  addon_id    TEXT PRIMARY KEY,
  version     TEXT NOT NULL,
  permissions TEXT NOT NULL, -- a JSON array, sorted
  approved_by BIGINT REFERENCES admins(id) ON DELETE SET NULL,
  approved_at BIGINT NOT NULL
);

-- +goose Down
DROP TABLE addon_grants;
