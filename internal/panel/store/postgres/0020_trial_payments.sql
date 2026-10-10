-- +goose Up
-- A priced trial is bought: its payment is of its own kind, applied like a new subscription
-- that also takes the account's trial.
ALTER TABLE payments DROP CONSTRAINT payments_kind_check;
ALTER TABLE payments ADD CONSTRAINT payments_kind_check CHECK (kind IN ('new', 'renew', 'package', 'trial'));

-- +goose Down
DELETE FROM payments WHERE kind = 'trial';
ALTER TABLE payments DROP CONSTRAINT payments_kind_check;
ALTER TABLE payments ADD CONSTRAINT payments_kind_check CHECK (kind IN ('new', 'renew', 'package'));
