-- name: ListSubTemplates :many
-- Every template with its text: what the subscriptions are served by.
SELECT * FROM sub_templates ORDER BY core, id;

-- name: ListSubTemplateHeads :many
-- The templates as the admin panel lists them: no text, its size.
SELECT id, core, name, is_default, OCTET_LENGTH(body)::BIGINT AS size, created_at, updated_at FROM sub_templates ORDER BY core, id;

-- name: GetSubTemplate :one
SELECT * FROM sub_templates WHERE id = $1;

-- name: CountSubTemplates :one
SELECT COUNT(*) FROM sub_templates WHERE core = $1;

-- name: CreateSubTemplate :one
INSERT INTO sub_templates (core, name, body, is_default, created_at, updated_at)
VALUES (sqlc.arg(core), sqlc.arg(name), sqlc.arg(body), false, sqlc.arg(at), sqlc.arg(at))
RETURNING *;

-- name: UpdateSubTemplate :one
UPDATE sub_templates SET name = $2, body = $3, updated_at = $4 WHERE id = $1
RETURNING *;

-- name: ClearSubTemplateDefault :exec
-- A core has at most one default (sub_templates_default): the old one goes first, in the
-- transaction that sets the new one.
UPDATE sub_templates SET is_default = false WHERE core = $1 AND is_default;

-- name: SetSubTemplateDefault :exec
UPDATE sub_templates SET is_default = true WHERE id = $1;

-- name: DeleteSubTemplate :execrows
DELETE FROM sub_templates WHERE id = $1;
