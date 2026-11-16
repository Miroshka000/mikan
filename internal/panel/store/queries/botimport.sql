-- name: MarkImportedAccount :execrows
-- Once per account and source: 0 rows, its balance was carried before.
INSERT INTO imported_accounts (source, tg_id, amount, at) VALUES ($1, $2, $3, $4)
ON CONFLICT (source, tg_id) DO NOTHING;

-- name: ImportedAccounts :many
SELECT tg_id FROM imported_accounts WHERE source = $1;

-- name: AddImportedPayment :execrows
INSERT INTO imported_payments (source, external_id, tg_id, kind, amount, currency, method, description, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (source, external_id) DO NOTHING;

-- name: ListImportedPayments :many
SELECT * FROM imported_payments WHERE tg_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2;

-- name: UsersOfLegacyTokens :many
SELECT token, user_id FROM legacy_sub_tokens WHERE token = ANY(sqlc.arg(tokens)::text[]);

-- name: UsersOfContacts :many
SELECT id, contact FROM users WHERE contact = ANY(sqlc.arg(contacts)::text[]) ORDER BY id;

-- name: TgLinksOfUsers :many
SELECT user_id, tg_id FROM tg_links WHERE user_id = ANY(sqlc.arg(ids)::bigint[]);

-- name: AddTgChatIfNew :exec
INSERT INTO tg_chats (tg_id, username, first_name, created_at, updated_at) VALUES ($1, $2, $3, $4, $4)
ON CONFLICT (tg_id) DO NOTHING;

-- name: LinkTgIfFree :execrows
-- A subscription with no owner gets one; one that has an owner keeps it.
INSERT INTO tg_links (user_id, tg_id, created_at) VALUES ($1, $2, $3) ON CONFLICT (user_id) DO NOTHING;

-- name: AddTgClient :exec
-- The account was a client since: the earlier of what mikan knew and since.
INSERT INTO tg_clients (tg_id, since) VALUES ($1, $2)
ON CONFLICT (tg_id) DO UPDATE SET since = LEAST(tg_clients.since, EXCLUDED.since);

-- name: UserNamesOf :many
SELECT id, name FROM users WHERE id = ANY(sqlc.arg(ids)::bigint[]);
