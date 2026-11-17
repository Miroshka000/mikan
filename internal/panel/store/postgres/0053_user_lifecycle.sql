-- +goose Up
-- The lifecycle of a subscription past its plain term (roadmap #10): the traffic a period
-- leaves, a term that waits for the first connection, the tariff that follows the current
-- one. Tables of their own, not columns of users and tariffs: the bot addon reads those rows
-- field for field (model.User and model.Tariff, held to golden files), and none of this is
-- the bot's to read.

-- A tariff's own rules. carry_over: whether the main traffic a period leaves goes into the
-- next one on a renewal or a switch to this tariff; NULL: as the panel's setting (domain
-- KeyLifecycle). start_on_connect: a subscription made on it starts its term at its first
-- connection, not when it is made. No row: both as the panel has them.
CREATE TABLE tariff_lifecycle (
  tariff_id        BIGINT PRIMARY KEY REFERENCES tariffs(id) ON DELETE CASCADE,
  carry_over       BOOLEAN,
  start_on_connect BOOLEAN NOT NULL DEFAULT false
);

-- A subscription whose term waits for its first connection. Its clock stands still: the
-- housekeeping moves its expiry, its traffic period and its packages on by the time since
-- frozen_at, so the term left stays whole, until its first counted traffic starts it.
CREATE TABLE user_starts (
  user_id   BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  frozen_at BIGINT NOT NULL
);

-- The tariff a subscription moves to once its term ends, once: the switch deletes the row.
-- term_days is the term on it; NULL: the tariff's own first term.
CREATE TABLE user_next_tariffs (
  user_id    BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  tariff_id  BIGINT NOT NULL REFERENCES tariffs(id) ON DELETE CASCADE,
  term_days  BIGINT CHECK (term_days BETWEEN 0 AND 3650),
  created_at BIGINT NOT NULL
);
CREATE INDEX user_next_tariffs_tariff ON user_next_tariffs (tariff_id);

-- The main traffic a period left that a renewal or a switch carried into the next one.
ALTER TABLE traffic_grants DROP CONSTRAINT traffic_grants_source_check;
ALTER TABLE traffic_grants ADD CONSTRAINT traffic_grants_source_check CHECK (source IN ('purchase', 'admin', 'gift', 'carry'));

-- +goose Down
DELETE FROM traffic_grants WHERE source = 'carry';
ALTER TABLE traffic_grants DROP CONSTRAINT traffic_grants_source_check;
ALTER TABLE traffic_grants ADD CONSTRAINT traffic_grants_source_check CHECK (source IN ('purchase', 'admin', 'gift'));
DROP TABLE user_next_tariffs;
DROP TABLE user_starts;
DROP TABLE tariff_lifecycle;
