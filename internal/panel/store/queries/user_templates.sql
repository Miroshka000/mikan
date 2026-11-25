-- name: ListUserTemplates :many
SELECT * FROM user_templates ORDER BY lower(name), id;

-- name: GetUserTemplate :one
SELECT * FROM user_templates WHERE id = $1;

-- name: CountUserTemplates :one
SELECT count(*) FROM user_templates;

-- name: CreateUserTemplate :one
INSERT INTO user_templates (name, tariff_id, traffic_limit, device_limit, speed_limit, term_days, starts_on_connect,
  folder_id, group_id, tags, note, name_pattern, created_at, updated_at)
VALUES (sqlc.arg(name), sqlc.arg(tariff_id), sqlc.narg(traffic_limit), sqlc.narg(device_limit), sqlc.narg(speed_limit),
  sqlc.narg(term_days), sqlc.narg(starts_on_connect), sqlc.narg(folder_id), sqlc.narg(group_id), sqlc.arg(tags),
  sqlc.arg(note), sqlc.arg(name_pattern), sqlc.arg(at), sqlc.arg(at))
RETURNING *;

-- name: UpdateUserTemplate :one
UPDATE user_templates SET name = sqlc.arg(name), tariff_id = sqlc.arg(tariff_id), traffic_limit = sqlc.narg(traffic_limit),
  device_limit = sqlc.narg(device_limit), speed_limit = sqlc.narg(speed_limit), term_days = sqlc.narg(term_days),
  starts_on_connect = sqlc.narg(starts_on_connect), folder_id = sqlc.narg(folder_id), group_id = sqlc.narg(group_id),
  tags = sqlc.arg(tags), note = sqlc.arg(note), name_pattern = sqlc.arg(name_pattern), updated_at = sqlc.arg(at)
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: DeleteUserTemplate :execrows
DELETE FROM user_templates WHERE id = $1;
