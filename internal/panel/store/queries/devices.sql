-- name: GetBoundDevice :one
SELECT * FROM bound_devices WHERE user_id = $1 AND hwid = $2;

-- name: GetBoundDeviceByID :one
SELECT * FROM bound_devices WHERE id = $1 AND user_id = $2;

-- name: ListBoundDevices :many
SELECT * FROM bound_devices WHERE user_id = $1 ORDER BY created_at, id;

-- name: ListIdleBoundDevices :many
SELECT * FROM bound_devices WHERE last_seen < $1;

-- name: CountBoundDevices :one
SELECT count(*) FROM bound_devices WHERE user_id = $1;

-- name: CreateBoundDevice :one
INSERT INTO bound_devices (user_id, hwid, slot_id, os, os_version, model, app, last_ip, created_at, last_seen)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: TouchBoundDevice :exec
UPDATE bound_devices SET os = $1, os_version = $2, model = $3, app = $4, last_ip = $5, last_seen = $6 WHERE id = $7;

-- name: DeleteBoundDevice :exec
DELETE FROM bound_devices WHERE id = $1;

-- name: DeleteBoundDevicesOf :exec
DELETE FROM bound_devices WHERE user_id = $1;

-- name: SetUserSlot :exec
-- A new own slot for the user, same subscription link (the shared device was unbound).
UPDATE users SET slot_id = $1, updated_at = $2 WHERE id = $3;

-- name: AddUnbind :exec
-- The subscriber unbound a device: it counts against the admin's limit.
INSERT INTO device_unbinds (user_id, at) VALUES ($1, $2);

-- name: ListUnbindsSince :many
-- The subscriber's unbinds after a moment, the oldest first.
SELECT at FROM device_unbinds WHERE user_id = $1 AND at > $2 ORDER BY at;

-- name: DeleteUnbindsBefore :exec
DELETE FROM device_unbinds WHERE at < $1;

-- name: LockUser :exec
-- Two unbinds of one subscriber at once must not both fit under the limit.
SELECT id FROM users WHERE id = $1 FOR UPDATE;

-- name: ListDeviceSlots :many
-- Slots of bound devices with an id: keys of their own, profile fetches of their own.
SELECT s.name AS slot_name, d.user_id, d.last_seen FROM bound_devices d JOIN slots s ON s.id = d.slot_id WHERE d.hwid != '';

-- name: SetBoundDeviceName :execrows
-- The device's own name; '' goes back to the one its app reports.
UPDATE bound_devices SET name = $1 WHERE id = $2 AND user_id = $3;

-- name: CreateDeviceBan :one
INSERT INTO device_bans (user_id, hwid, label, admin_id, banned_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (user_id, hwid) DO UPDATE SET label = EXCLUDED.label, admin_id = EXCLUDED.admin_id, banned_at = EXCLUDED.banned_at, until = NULL
RETURNING *;

-- name: HoldUnbound :exec
-- A device its subscriber unbound may not bind again until a moment. The admin's ban of
-- the same device stays for good.
INSERT INTO device_bans (user_id, hwid, label, banned_at, until)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (user_id, hwid) DO UPDATE SET label = EXCLUDED.label, banned_at = EXCLUDED.banned_at, until = EXCLUDED.until
WHERE device_bans.until IS NOT NULL;

-- name: ActiveDeviceBan :one
-- The ban on a device now: until is NULL for the admin's ban, a moment for an unbound one.
SELECT until FROM device_bans WHERE user_id = $1 AND hwid = $2 AND (until IS NULL OR until > $3);

-- name: DeleteExpiredBans :exec
DELETE FROM device_bans WHERE until IS NOT NULL AND until <= $1;

-- name: ListDeviceBans :many
SELECT b.id, b.user_id, b.hwid, b.label, b.admin_id, b.banned_at, b.until, COALESCE(a.username, '')::text AS admin_name
FROM device_bans b LEFT JOIN admins a ON a.id = b.admin_id
WHERE b.user_id = $1 AND (b.until IS NULL OR b.until > $2) ORDER BY b.banned_at DESC, b.id DESC;

-- name: DeleteDeviceBan :one
DELETE FROM device_bans WHERE id = $1 AND user_id = $2 RETURNING *;

-- name: AddSlotsTraffic :exec
-- Adds a batch's traffic to its slots, in slot order so two nodes' batches take turns.
INSERT INTO slot_traffic (slot_id, up, down)
SELECT s.id, v.up, v.down
FROM (SELECT unnest(sqlc.arg(names)::text[]) AS name, unnest(sqlc.arg(up)::bigint[]) AS up, unnest(sqlc.arg(down)::bigint[]) AS down) AS v
JOIN slots s ON s.name = v.name
ORDER BY s.id
ON CONFLICT (slot_id) DO UPDATE SET up = slot_traffic.up + excluded.up, down = slot_traffic.down + excluded.down;

-- name: ListBoundDeviceTraffic :many
-- What each of a user's bound devices used since it was bound.
SELECT d.id AS device_id, COALESCE(t.up, 0)::bigint AS up, COALESCE(t.down, 0)::bigint AS down
FROM bound_devices d LEFT JOIN slot_traffic t ON t.slot_id = d.slot_id WHERE d.user_id = $1;
