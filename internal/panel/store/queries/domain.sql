-- name: ListTariffs :many
SELECT * FROM tariffs WHERE archived = 0 ORDER BY sort, id;

-- name: GetTariff :one
SELECT * FROM tariffs WHERE id = $1;

-- name: CountTariffs :one
SELECT count(*) FROM tariffs;

-- name: CreateTariff :one
INSERT INTO tariffs (name, traffic_limit, duration_days, device_limit, reset_strategy, price_label, sort, created_at, billing_day, price_stars, price_rub, on_sale)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING *;

-- name: UpdateTariff :one
UPDATE tariffs
SET name = $1, traffic_limit = $2, duration_days = $3, device_limit = $4, reset_strategy = $5, price_label = $6, sort = $7, billing_day = $8,
    price_stars = $9, price_rub = $10, on_sale = $11
WHERE id = $12
RETURNING *;

-- name: ListTariffTerms :many
SELECT * FROM tariff_terms WHERE tariff_id = $1 ORDER BY sort, id;

-- name: ListAllTariffTerms :many
SELECT * FROM tariff_terms ORDER BY tariff_id, sort, id;

-- name: DeleteTariffTerms :exec
DELETE FROM tariff_terms WHERE tariff_id = $1;

-- name: AddTariffTerm :exec
INSERT INTO tariff_terms (tariff_id, days, price_stars, price_rub, sort) VALUES ($1, $2, $3, $4, $5);

-- name: ArchiveTariff :execrows
UPDATE tariffs SET archived = 1 WHERE id = $1;

-- name: CountSlotsByState :many
SELECT state, count(*) AS n FROM slots GROUP BY state;

-- name: InsertSlot :exec
INSERT INTO slots (name, uuid, secret, state, created_at) VALUES ($1, $2, $3, 'free', $4);

-- name: SlotCounter :one
-- The last slot number handed out: slots purged from the top do not give theirs back.
SELECT last FROM slot_counter WHERE id = 1;

-- name: TakeFreeSlot :one
UPDATE slots SET state = 'assigned'
WHERE id = (SELECT id FROM slots WHERE state = 'free' ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED) AND state = 'free'
RETURNING *;

-- name: BurnSlot :exec
UPDATE slots SET state = 'burned', burned_at = $1 WHERE id = $2;

-- name: DeleteBurnedSlots :exec
DELETE FROM slots WHERE state = 'burned' AND id NOT IN (SELECT slot_id FROM users WHERE slot_id IS NOT NULL)
  AND id NOT IN (SELECT slot_id FROM bound_devices);

-- name: ListSlots :many
SELECT * FROM slots ORDER BY id;

-- name: GetSlot :one
SELECT * FROM slots WHERE id = $1;

-- name: ListSlotUsers :many
-- Every slot that works for a user: the own one and those of bound devices.
SELECT s.name AS slot_name, u.id AS user_id FROM slots s JOIN users u ON u.slot_id = s.id
UNION
SELECT s.name AS slot_name, d.user_id AS user_id FROM slots s JOIN bound_devices d ON d.slot_id = s.id;

-- name: ListUserSlots :many
-- The same for one user: what an answer about a single user needs, not the whole table.
SELECT s.name FROM slots s JOIN users u ON u.slot_id = s.id WHERE u.id = sqlc.arg(user_id)
UNION
SELECT s.name FROM slots s JOIN bound_devices d ON d.slot_id = s.id WHERE d.user_id = sqlc.arg(user_id);

-- name: ListBoundDeviceSlots :many
-- The names of the slots of a user's bound devices, by device id.
SELECT d.id AS device_id, s.name AS slot_name FROM bound_devices d JOIN slots s ON s.id = d.slot_id WHERE d.user_id = $1;

-- name: CreateUser :one
INSERT INTO users (name, contact, note, tags, status, tariff_id, traffic_limit, device_limit, reset_strategy,
                   period_days, period_start, expires_at, inbounds, sub_token, slot_id, created_at, updated_at, billing_day)
VALUES ($1, $2, $3, $4, 'active', $5, $6, $7, $8, $9, $10, $11, NULL, $12, $13, $14, $15, $16)
RETURNING *;

-- name: GetUser :one
SELECT * FROM users WHERE id = $1;

-- name: GetUserBySubToken :one
SELECT * FROM users WHERE sub_token = $1;

-- name: ListUsers :many
SELECT * FROM users ORDER BY id DESC;

-- name: UpdateUser :one
UPDATE users
SET name = $1, contact = $2, note = $3, tags = $4, status = $5, tariff_id = $6, traffic_limit = $7, device_limit = $8,
    reset_strategy = $9, period_days = $10, period_start = $11, expires_at = $12, inbounds = $13, updated_at = $14, billing_day = $15
WHERE id = $16
RETURNING *;

-- name: SetUserCredentials :exec
UPDATE users SET slot_id = $1, sub_token = $2, updated_at = $3 WHERE id = $4;

-- name: ResetUserTraffic :exec
UPDATE users SET used_up = 0, used_down = 0, period_start = $1, updated_at = $2 WHERE id = $3;

-- name: AddUserTraffic :exec
UPDATE users
SET used_up = used_up + sqlc.arg(up), used_down = used_down + sqlc.arg(down),
    total_up = total_up + sqlc.arg(up), total_down = total_down + sqlc.arg(down)
WHERE id = sqlc.arg(id);

-- name: AddTrafficHourly :exec
INSERT INTO traffic_hourly (user_id, hour, up, down) VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id, hour) DO UPDATE SET up = traffic_hourly.up + excluded.up, down = traffic_hourly.down + excluded.down;

-- name: AddTrafficDaily :exec
INSERT INTO traffic_daily (user_id, day, up, down) VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id, day) DO UPDATE SET up = traffic_daily.up + excluded.up, down = traffic_daily.down + excluded.down;

-- name: UserTrafficHourly :many
SELECT hour, up, down FROM traffic_hourly WHERE user_id = $1 AND hour >= $2 ORDER BY hour;

-- name: UserTrafficDaily :many
SELECT day, up, down FROM traffic_daily WHERE user_id = $1 AND day >= $2 ORDER BY day;

-- name: TotalTrafficHourly :many
SELECT hour, CAST(sum(up) AS BIGINT) AS up, CAST(sum(down) AS BIGINT) AS down
FROM traffic_hourly WHERE hour >= $1 GROUP BY hour ORDER BY hour;

-- name: TotalTrafficDaily :many
SELECT day, CAST(sum(up) AS BIGINT) AS up, CAST(sum(down) AS BIGINT) AS down
FROM traffic_daily WHERE day >= $1 GROUP BY day ORDER BY day;

-- name: TopUsersByTraffic :many
SELECT u.id, u.name, CAST(sum(d.up + d.down) AS BIGINT) AS bytes
FROM traffic_daily d JOIN users u ON u.id = d.user_id
WHERE d.day >= $1
GROUP BY u.id, u.name ORDER BY bytes DESC LIMIT CAST(sqlc.arg(lim) AS BIGINT);

-- name: PruneTrafficHourly :exec
DELETE FROM traffic_hourly WHERE hour < $1;

-- name: PruneTrafficDaily :exec
DELETE FROM traffic_daily WHERE day < $1;

-- name: UpsertDevice :exec
INSERT INTO devices (user_id, ip, first_seen, last_seen) VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id, ip) DO UPDATE SET last_seen = excluded.last_seen;

-- name: ListUserDevices :many
SELECT * FROM devices WHERE user_id = $1 ORDER BY last_seen DESC;

-- name: PruneDevices :exec
DELETE FROM devices WHERE last_seen < $1;

-- name: ListInbounds :many
SELECT * FROM inbounds ORDER BY id;

-- name: ListNodeInbounds :many
SELECT * FROM inbounds WHERE node_id = $1 ORDER BY id;

-- name: GetInbound :one
SELECT * FROM inbounds WHERE id = $1;

-- name: CreateInbound :one
INSERT INTO inbounds (node_id, name, preset, port, enabled, settings, config, created_at, updated_at)
VALUES ($1, $2, $3, $4, 1, '{}', $5, $6, $7)
RETURNING *;

-- name: UpdateInbound :one
UPDATE inbounds SET port = $1, enabled = $2, config = $3, display_name = $4, updated_at = $5 WHERE id = $6 RETURNING *;

-- name: SetInboundConfig :exec
UPDATE inbounds SET config = $1 WHERE id = $2;

-- name: DeleteInbound :exec
DELETE FROM inbounds WHERE id = $1;

-- name: GetNodeState :one
SELECT value FROM node_state WHERE key = $1;

-- name: SetNodeState :exec
INSERT INTO node_state (key, value) VALUES ($1, $2)
ON CONFLICT (key) DO UPDATE SET value = excluded.value;

-- name: DeleteNodeStateOf :exec
DELETE FROM node_state WHERE key LIKE '%/' || CAST(sqlc.arg(node_id) AS TEXT);
