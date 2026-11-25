-- +goose Up
-- What a host adds to how clients connect (#01): the ALPN they offer, the IP version they
-- reach its address by, multiplexing (sing-mux: on, its protocol, padding) and a CDN in
-- front of it that shows its own certificate. Each empty (false) keeps what clients get
-- without it. The default host has them too, as its own: the template has no such keys,
-- so they are written only through the hosts, never by syncDefaultHost.
ALTER TABLE inbound_hosts
  ADD COLUMN alpn         TEXT[]  NOT NULL DEFAULT '{}'
    CHECK (alpn <@ ARRAY['h3', 'h2', 'http/1.1']::TEXT[] AND cardinality(alpn) <= 3),
  ADD COLUMN ip_version   TEXT    NOT NULL DEFAULT ''
    CHECK (ip_version IN ('', 'ipv4', 'ipv6', 'ipv4-prefer', 'ipv6-prefer')),
  ADD COLUMN mux          BOOLEAN NOT NULL DEFAULT false,
  ADD COLUMN mux_protocol TEXT    NOT NULL DEFAULT ''
    CHECK (mux_protocol IN ('', 'smux', 'yamux', 'h2mux')),
  ADD COLUMN mux_padding  BOOLEAN NOT NULL DEFAULT false,
  ADD COLUMN cdn          BOOLEAN NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE inbound_hosts
  DROP COLUMN alpn, DROP COLUMN ip_version, DROP COLUMN mux, DROP COLUMN mux_protocol,
  DROP COLUMN mux_padding, DROP COLUMN cdn;
