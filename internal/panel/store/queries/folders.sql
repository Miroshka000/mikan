-- name: ListFolders :many
SELECT * FROM user_folders ORDER BY sort, id;

-- name: GetFolder :one
SELECT * FROM user_folders WHERE id = $1;

-- name: CreateFolder :one
-- A new folder goes last.
INSERT INTO user_folders (name, color, emoji, sort, created_at)
VALUES ($1, $2, $3, (SELECT COALESCE(MAX(sort), 0) + 1 FROM user_folders), $4)
RETURNING *;

-- name: UpdateFolder :one
UPDATE user_folders SET name = $1, color = $2, emoji = $3 WHERE id = $4 RETURNING *;

-- name: SetFolderSort :exec
UPDATE user_folders SET sort = $1 WHERE id = $2;

-- name: LockFolderUsers :many
-- The folder's users, locked in id order like every other writer of users rows: the
-- foreign key would otherwise lock them in index order and deadlock with traffic batches.
SELECT id FROM users WHERE folder_id = $1 ORDER BY id FOR NO KEY UPDATE;

-- name: DeleteFolder :execrows
-- Its users stay: their folder is cleared by the foreign key.
DELETE FROM user_folders WHERE id = $1;

-- name: CountFolderUsers :many
-- How many users each folder holds, hidden ones too.
SELECT folder_id, count(*) AS n FROM users WHERE folder_id IS NOT NULL GROUP BY folder_id;

-- name: LockFolder :one
-- The folder, kept from being deleted until the transaction ends: a user moved into it
-- cannot meet a folder that is gone.
SELECT id FROM user_folders WHERE id = $1 FOR KEY SHARE;

-- name: SetUserHidden :exec
UPDATE users SET hidden = sqlc.arg(hidden)::bigint, updated_at = sqlc.arg(updated_at) WHERE id = sqlc.arg(id);

-- name: SetUserFolder :exec
UPDATE users SET folder_id = sqlc.narg(folder_id)::bigint, updated_at = sqlc.arg(updated_at) WHERE id = sqlc.arg(id);

-- name: SetUsersHidden :exec
UPDATE users SET hidden = sqlc.arg(hidden)::bigint, updated_at = sqlc.arg(updated_at) WHERE id = ANY(sqlc.arg(ids)::bigint[]);

-- name: SetUsersFolder :exec
-- No folder (NULL) takes the users out of theirs.
UPDATE users SET folder_id = sqlc.narg(folder_id)::bigint, updated_at = sqlc.arg(updated_at) WHERE id = ANY(sqlc.arg(ids)::bigint[]);
