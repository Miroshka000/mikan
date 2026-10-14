-- name: ListSubDocs :many
SELECT * FROM sub_docs ORDER BY position, id;

-- name: ListPublishedSubDocs :many
-- What the subscription page lists: the published pages without their text.
SELECT id, title, emoji, platform FROM sub_docs WHERE published = 1 ORDER BY position, id;

-- name: GetPublishedSubDoc :one
SELECT * FROM sub_docs WHERE id = $1 AND published = 1;

-- name: CountSubDocs :one
SELECT COUNT(*) FROM sub_docs;

-- name: CreateSubDoc :one
-- A new page goes last.
INSERT INTO sub_docs (title, emoji, body, platform, position, published, created_at, updated_at)
VALUES (sqlc.arg(title), sqlc.arg(emoji), sqlc.arg(body), sqlc.arg(platform), (SELECT COALESCE(MAX(d.position), 0) + 1 FROM sub_docs d), sqlc.arg(published), sqlc.arg(at), sqlc.arg(at))
RETURNING *;

-- name: UpdateSubDoc :one
UPDATE sub_docs SET title = $2, emoji = $3, body = $4, platform = $5, published = $6, updated_at = $7
WHERE id = $1
RETURNING *;

-- name: SetSubDocPosition :execrows
UPDATE sub_docs SET position = $2 WHERE id = $1;

-- name: DeleteSubDoc :execrows
DELETE FROM sub_docs WHERE id = $1;

-- name: GetSubAsset :one
SELECT * FROM sub_assets WHERE name = $1;

-- name: ListSubAssetMeta :many
-- What the page needs to link the images: no bytes.
SELECT name, content_type, hash, OCTET_LENGTH(data)::BIGINT AS size, updated_at FROM sub_assets ORDER BY name;

-- name: PutSubAsset :exec
INSERT INTO sub_assets (name, content_type, data, hash, updated_at) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (name) DO UPDATE SET content_type = excluded.content_type, data = excluded.data, hash = excluded.hash, updated_at = excluded.updated_at;

-- name: DeleteSubAsset :execrows
DELETE FROM sub_assets WHERE name = $1;
