-- +goose Up
-- The referencing side of foreign keys that had none: deleting a tariff, a package or a
-- node set these to NULL with a scan of the whole table, and the tariff's users are also
-- looked up by it (ListUserIDsOfTariff, SetTariffUsersPool). Partial: most rows have none.
-- Not payments.tariff_id: tariffs are archived, not deleted, and an index there would win
-- the plan of FindOpenPayment over payments_tg.
CREATE INDEX users_tariff ON users (tariff_id) WHERE tariff_id IS NOT NULL;
CREATE INDEX payments_package ON payments (package_id) WHERE package_id IS NOT NULL;
CREATE INDEX traffic_grants_package ON traffic_grants (package_id) WHERE package_id IS NOT NULL;
CREATE INDEX torrent_hits_node ON torrent_hits (node_id) WHERE node_id IS NOT NULL;

-- +goose Down
DROP INDEX torrent_hits_node;
DROP INDEX traffic_grants_package;
DROP INDEX payments_package;
DROP INDEX users_tariff;
