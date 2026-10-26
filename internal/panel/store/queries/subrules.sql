-- name: ListSubRules :many
SELECT * FROM sub_rules ORDER BY position;

-- name: DeleteSubRules :exec
DELETE FROM sub_rules;

-- name: InsertSubRule :exec
INSERT INTO sub_rules (position, name, enabled, cond, action) VALUES ($1, $2, $3, $4, $5);

-- name: ListBlockedApps :many
SELECT app FROM sub_blocked_apps ORDER BY app;

-- name: DeleteBlockedApps :exec
DELETE FROM sub_blocked_apps;

-- name: InsertBlockedApp :exec
INSERT INTO sub_blocked_apps (app) VALUES ($1);

-- name: AddSubLog :execrows
-- One fetch of a subscription. A link that is hammered must not fill the table: past
-- max_per_minute rows in the minute before the new one's time the row is not written.
INSERT INTO sub_log (user_id, at, ip, agent, app, rule, format, outcome)
SELECT sqlc.arg(user_id)::bigint, sqlc.arg(at)::bigint, sqlc.arg(ip)::text, sqlc.arg(agent)::text, sqlc.arg(app)::text,
       sqlc.arg(rule)::text, sqlc.arg(format)::text, sqlc.arg(outcome)::text
WHERE (SELECT COUNT(*) FROM sub_log WHERE user_id = sqlc.arg(user_id)::bigint AND at > sqlc.arg(at)::bigint - 60) < sqlc.arg(max_per_minute)::bigint;

-- name: ListSubLog :many
SELECT * FROM sub_log WHERE user_id = $1 ORDER BY at DESC, id DESC LIMIT $2;

-- name: PruneSubLog :execrows
DELETE FROM sub_log WHERE at < $1;
