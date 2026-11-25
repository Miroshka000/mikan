-- name: ListSubTemplates :many
-- Every template with its text or routing: what the subscriptions are served by.
SELECT * FROM sub_templates ORDER BY core, id;

-- name: ListSubTemplateHeads :many
-- The templates as the admin panel lists them: no text, its size, and whether the template is
-- a simple one (a routing of its own, no text).
SELECT id, core, name, is_default, OCTET_LENGTH(body)::BIGINT AS size, (routing IS NOT NULL)::BOOLEAN AS simple, created_at, updated_at
FROM sub_templates ORDER BY core, id;

-- name: ListSimpleSubTemplates :many
-- The simple templates with their routing, without the texts of the others.
SELECT id, name, routing FROM sub_templates WHERE routing IS NOT NULL ORDER BY id;

-- name: GetSubTemplate :one
SELECT * FROM sub_templates WHERE id = $1;

-- name: CountSubTemplates :one
SELECT COUNT(*) FROM sub_templates WHERE core = $1;

-- name: CreateSubTemplate :one
-- A template of text (routing NULL) or a simple one (routing set, body empty).
INSERT INTO sub_templates (core, name, body, routing, is_default, created_at, updated_at)
VALUES (sqlc.arg(core), sqlc.arg(name), sqlc.arg(body), sqlc.narg(routing), false, sqlc.arg(at), sqlc.arg(at))
RETURNING *;

-- name: UpdateSubTemplate :one
UPDATE sub_templates SET name = $2, body = $3, routing = $4, updated_at = $5 WHERE id = $1
RETURNING *;

-- name: ClearSubTemplateDefault :exec
-- A core has at most one default (sub_templates_default): the old one goes first, in the
-- transaction that sets the new one.
UPDATE sub_templates SET is_default = false WHERE core = $1 AND is_default;

-- name: SetSubTemplateDefault :exec
UPDATE sub_templates SET is_default = true WHERE id = $1;

-- name: DeleteSubTemplate :execrows
DELETE FROM sub_templates WHERE id = $1;

-- name: LockSubTemplates :exec
-- Serialises what changes the templates or the rules that name them: the count per core
-- against the limit, the one default per core, and a template deleted while a rule that
-- names it is saved. Held to the end of the transaction.
SELECT pg_advisory_xact_lock(hashtextextended('mikan-sub-templates', 0));
