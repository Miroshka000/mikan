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

-- name: SetUserUnboundAt :exec
UPDATE users SET unbound_at = $1 WHERE id = $2;

-- name: ListDeviceSlots :many
-- Slots of bound devices with an id: keys of their own, profile fetches of their own.
SELECT s.name AS slot_name, d.user_id, d.last_seen FROM bound_devices d JOIN slots s ON s.id = d.slot_id WHERE d.hwid != '';

-- name: SetBoundDeviceName :execrows
-- The device's own name; '' goes back to the one its app reports.
UPDATE bound_devices SET name = $1 WHERE id = $2 AND user_id = $3;

-- name: CreateDeviceBan :one
INSERT INTO device_bans (user_id, hwid, label, admin_id, banned_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (user_id, hwid) DO UPDATE SET label = EXCLUDED.label, admin_id = EXCLUDED.admin_id, banned_at = EXCLUDED.banned_at
RETURNING *;

-- name: DeviceBanned :one
SELECT EXISTS (SELECT 1 FROM device_bans WHERE user_id = $1 AND hwid = $2);

-- name: ListDeviceBans :many
SELECT b.id, b.user_id, b.hwid, b.label, b.admin_id, b.banned_at, COALESCE(a.username, '')::text AS admin_name
FROM device_bans b LEFT JOIN admins a ON a.id = b.admin_id
WHERE b.user_id = $1 ORDER BY b.banned_at DESC, b.id DESC;

-- name: DeleteDeviceBan :one
DELETE FROM device_bans WHERE id = $1 AND user_id = $2 RETURNING *;
