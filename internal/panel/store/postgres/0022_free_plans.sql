-- +goose Up
-- The free plan (billing.FreePlan): unlike the trial, a Telegram account takes it again once
-- its previous free subscription has ended. One row per account, its last free subscription
-- and how many it took; the row is locked on the transaction that makes the subscription,
-- so two taps (or the bot and the Mini App at once) make one.
CREATE TABLE free_plans (
  tg_id      BIGINT PRIMARY KEY,
  user_id    BIGINT REFERENCES users(id) ON DELETE SET NULL,
  tariff_id  BIGINT REFERENCES tariffs(id) ON DELETE SET NULL,
  taken      BIGINT NOT NULL DEFAULT 0,
  created_at BIGINT NOT NULL
);
-- Deleting a user sets its row's user_id to NULL: without this, a scan of the table.
CREATE INDEX free_plans_user ON free_plans (user_id) WHERE user_id IS NOT NULL;

-- A subscription the free plan gave is told apart from the trial's on the users' list.
ALTER TABLE users DROP CONSTRAINT users_source_check;
ALTER TABLE users ADD CONSTRAINT users_source_check CHECK (source IN ('admin', 'bot', 'trial', 'import', 'free'));

-- +goose Down
-- The free subscriptions stay, as the trial's: the closest origin the old check knows.
UPDATE users SET source = 'trial' WHERE source = 'free';
ALTER TABLE users DROP CONSTRAINT users_source_check;
ALTER TABLE users ADD CONSTRAINT users_source_check CHECK (source IN ('admin', 'bot', 'trial', 'import'));
DROP TABLE free_plans;
