-- +goose Up
-- A speed cap in Mbit/s, each way: a tariff's goes to its users like the device limit,
-- and the admin may set or lift a user's own. NULL: no cap.
ALTER TABLE tariffs ADD COLUMN speed_limit BIGINT CHECK (speed_limit > 0);
ALTER TABLE users ADD COLUMN speed_limit BIGINT CHECK (speed_limit > 0);

-- Fair share on a node: its channel (Mbit/s) is split evenly between the users moving
-- traffic through it at the moment; a user's own cap stays the ceiling.
ALTER TABLE nodes ADD COLUMN fair_share BIGINT NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN channel_mbps BIGINT CHECK (channel_mbps > 0);

-- +goose Down
ALTER TABLE nodes DROP COLUMN channel_mbps;
ALTER TABLE nodes DROP COLUMN fair_share;
ALTER TABLE users DROP COLUMN speed_limit;
ALTER TABLE tariffs DROP COLUMN speed_limit;
