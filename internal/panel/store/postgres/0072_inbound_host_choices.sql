-- +goose Up
-- More addresses and TLS names of a host (#01): each client gets one of the host's own and
-- these, the same one at every update (chosen by its subscription's token), so that clients
-- spread over them. Empty: the host's own alone. The default host has them too, as its own:
-- the template holds one address and one name.
ALTER TABLE inbound_hosts
  ADD COLUMN addresses TEXT[] NOT NULL DEFAULT '{}' CHECK (cardinality(addresses) <= 16),
  ADD COLUMN snis      TEXT[] NOT NULL DEFAULT '{}' CHECK (cardinality(snis) <= 16);

-- +goose Down
ALTER TABLE inbound_hosts DROP COLUMN addresses, DROP COLUMN snis;
