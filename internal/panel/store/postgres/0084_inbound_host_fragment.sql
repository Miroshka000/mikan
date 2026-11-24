-- +goose Up
-- How a host's clients split the TLS ClientHello they send (#01): into TLS records sent at
-- once ('record'), or those records each in a TCP packet of its own with a pause between
-- ('tcp'); '' leaves it whole. A host's own field, as alpn and the rest of 0070: the
-- default host has it too, written only through the hosts, never by syncDefaultHost.
ALTER TABLE inbound_hosts
  ADD COLUMN fragment TEXT NOT NULL DEFAULT ''
    CHECK (fragment IN ('', 'record', 'tcp'));

-- +goose Down
ALTER TABLE inbound_hosts DROP COLUMN fragment;
