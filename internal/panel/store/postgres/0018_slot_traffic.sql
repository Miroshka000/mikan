-- +goose Up
-- Traffic by slot since it was handed out: a bound device has a slot of its own, so this
-- is what each device used (the shared place of apps without a device id included). A
-- table of its own, so counting never waits on a device row an unbind is deleting.
CREATE TABLE slot_traffic (
  slot_id BIGINT PRIMARY KEY REFERENCES slots(id) ON DELETE CASCADE,
  up      BIGINT NOT NULL DEFAULT 0,
  down    BIGINT NOT NULL DEFAULT 0
);

-- +goose Down
DROP TABLE slot_traffic;
