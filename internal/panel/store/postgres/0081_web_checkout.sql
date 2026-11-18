-- +goose Up
ALTER TABLE users DROP CONSTRAINT users_source_check;
ALTER TABLE users ADD CONSTRAINT users_source_check CHECK (source IN ('admin', 'bot', 'trial', 'import', 'free', 'web'));

-- Keep the receipt email until payment applies and the link hash until it rotates.
CREATE TABLE web_checkouts (
  payment_id BIGINT PRIMARY KEY REFERENCES payments(id) ON DELETE CASCADE,
  receipt_email TEXT NOT NULL,
  token_hash TEXT NOT NULL DEFAULT ''
);

-- Only one request at a time may open an invoice for a checkout.
CREATE TABLE web_invoice_leases (
  payment_id BIGINT PRIMARY KEY REFERENCES payments(id) ON DELETE CASCADE,
  lease_until BIGINT NOT NULL
);

-- +goose Down
DROP TABLE web_invoice_leases;
DROP TABLE web_checkouts;
UPDATE users SET source = 'admin' WHERE source = 'web';
ALTER TABLE users DROP CONSTRAINT users_source_check;
ALTER TABLE users ADD CONSTRAINT users_source_check CHECK (source IN ('admin', 'bot', 'trial', 'import', 'free'));
