-- +goose Up
-- A priced trial is bought: its payment is of its own kind, applied like a new subscription
-- that also takes the account's trial.
ALTER TABLE payments DROP CONSTRAINT payments_kind_check;
ALTER TABLE payments ADD CONSTRAINT payments_kind_check CHECK (kind IN ('new', 'renew', 'package', 'trial'));

-- +goose Down
-- Trial payments stay: they are money taken, with the subscriptions they gave. The old check
-- comes back for new rows only (NOT VALID), so the rows it does not know do not stop the
-- rollback.
ALTER TABLE payments DROP CONSTRAINT payments_kind_check;
ALTER TABLE payments ADD CONSTRAINT payments_kind_check CHECK (kind IN ('new', 'renew', 'package')) NOT VALID;
