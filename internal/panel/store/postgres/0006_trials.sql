-- +goose Up
-- The free trial a Telegram account takes once (issue #34): the row is taken on the same
-- transaction that makes the subscription, so a second tap or a second chat finds it.
CREATE TABLE trials (
  tg_id      BIGINT PRIMARY KEY,
  user_id    BIGINT REFERENCES users(id) ON DELETE SET NULL,
  tariff_id  BIGINT REFERENCES tariffs(id) ON DELETE SET NULL,
  created_at BIGINT NOT NULL
);

-- +goose Down
DROP TABLE trials;
