-- +goose Up
-- What the admin keeps about a node's server at its provider: who hosts it, when it is paid
-- for and how much traffic the plan gives. pay_day is a UTC day (unix time / 86400) of a
-- payment, 0 none; pay_months repeats it every so many months, 0 once. remind_days before
-- the payment the admin is told, 0 never. traffic_limit is the plan's bytes a period, 0 none;
-- the period starts on reset_day of the month (the month's last day when it is shorter), and
-- the admin is told once a period when the node has carried traffic_alert percent of it.
ALTER TABLE nodes ADD COLUMN billing_provider TEXT NOT NULL DEFAULT '';
ALTER TABLE nodes ADD COLUMN pay_day BIGINT NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN pay_months BIGINT NOT NULL DEFAULT 1;
ALTER TABLE nodes ADD COLUMN remind_days BIGINT NOT NULL DEFAULT 3;
ALTER TABLE nodes ADD COLUMN traffic_limit BIGINT NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN reset_day BIGINT NOT NULL DEFAULT 1;
ALTER TABLE nodes ADD COLUMN traffic_alert BIGINT NOT NULL DEFAULT 80;

-- +goose Down
ALTER TABLE nodes DROP COLUMN traffic_alert;
ALTER TABLE nodes DROP COLUMN reset_day;
ALTER TABLE nodes DROP COLUMN traffic_limit;
ALTER TABLE nodes DROP COLUMN remind_days;
ALTER TABLE nodes DROP COLUMN pay_months;
ALTER TABLE nodes DROP COLUMN pay_day;
ALTER TABLE nodes DROP COLUMN billing_provider;
