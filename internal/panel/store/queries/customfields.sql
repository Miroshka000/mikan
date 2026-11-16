-- name: ListCustomFields :many
SELECT * FROM custom_fields ORDER BY scope, sort, id;

-- name: GetCustomField :one
SELECT * FROM custom_fields WHERE id = $1;

-- name: CreateCustomField :one
-- A new field goes last among its scope's.
INSERT INTO custom_fields (scope, key, label, kind, sort, created_at)
VALUES (sqlc.arg(scope), sqlc.arg(key), sqlc.arg(label), sqlc.arg(kind),
        (SELECT COALESCE(MAX(sort), 0) + 1 FROM custom_fields WHERE scope = sqlc.arg(scope)), sqlc.arg(created_at))
RETURNING *;

-- name: RenameCustomField :one
UPDATE custom_fields SET label = $1 WHERE id = $2 RETURNING *;

-- name: SetCustomFieldSort :exec
UPDATE custom_fields SET sort = $1 WHERE id = $2;

-- name: DeleteCustomField :execrows
-- Its values go with it.
DELETE FROM custom_fields WHERE id = $1;

-- name: CountCustomFieldValues :many
-- How many users or nodes have a value in each field.
SELECT field_id, count(*) AS n FROM (
  SELECT field_id FROM user_field_values
  UNION ALL
  SELECT field_id FROM node_field_values
) v GROUP BY field_id;

-- name: UserFieldValues :many
-- The values of the users of ids; all users when ids is NULL.
SELECT v.user_id, f.key, v.value FROM user_field_values v JOIN custom_fields f ON f.id = v.field_id
WHERE sqlc.narg(ids)::bigint[] IS NULL OR v.user_id = ANY(sqlc.narg(ids)::bigint[]);

-- name: NodeFieldValues :many
SELECT v.node_id, f.key, v.value FROM node_field_values v JOIN custom_fields f ON f.id = v.field_id;

-- name: SetUserFieldValue :exec
INSERT INTO user_field_values (user_id, field_id, value) VALUES ($1, $2, $3)
ON CONFLICT (user_id, field_id) DO UPDATE SET value = EXCLUDED.value;

-- name: ClearUserFieldValue :exec
DELETE FROM user_field_values WHERE user_id = $1 AND field_id = $2;

-- name: SetNodeFieldValue :exec
INSERT INTO node_field_values (node_id, field_id, value) VALUES ($1, $2, $3)
ON CONFLICT (node_id, field_id) DO UPDATE SET value = EXCLUDED.value;

-- name: ClearNodeFieldValue :exec
DELETE FROM node_field_values WHERE node_id = $1 AND field_id = $2;

-- name: LockCustomFields :many
-- The fields of ids that still exist, kept from being deleted until the transaction ends: a
-- value written for one of them cannot meet a field that is gone.
SELECT id FROM custom_fields WHERE id = ANY(sqlc.arg(ids)::bigint[]) ORDER BY id FOR KEY SHARE;

-- name: NodeFieldValuesOf :many
SELECT f.key, v.value FROM node_field_values v JOIN custom_fields f ON f.id = v.field_id WHERE v.node_id = $1;
