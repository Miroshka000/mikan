-- name: ListGroups :many
SELECT * FROM user_groups ORDER BY sort, id;

-- name: GetGroup :one
SELECT * FROM user_groups WHERE id = $1;

-- name: CreateGroup :one
-- A new group goes last.
INSERT INTO user_groups (name, color, emoji, sort, created_at)
VALUES ($1, $2, $3, (SELECT COALESCE(MAX(sort), 0) + 1 FROM user_groups), $4)
RETURNING *;

-- name: UpdateGroup :one
UPDATE user_groups SET name = $1, color = $2, emoji = $3 WHERE id = $4 RETURNING *;

-- name: SetGroupSort :exec
UPDATE user_groups SET sort = $1 WHERE id = $2;

-- name: LockGroups :many
-- The groups, kept from being deleted until the transaction ends: a client, a tariff or an
-- address given one of them cannot meet a group that is gone. In id order, as every writer
-- takes them.
SELECT id FROM user_groups WHERE id = ANY(sqlc.arg(ids)::bigint[]) ORDER BY id FOR KEY SHARE;

-- name: LockGroupForDelete :one
SELECT id FROM user_groups WHERE id = $1 FOR UPDATE;

-- name: DeleteGroup :execrows
-- Its clients and tariffs stay: their links to it go with it.
DELETE FROM user_groups WHERE id = $1;

-- name: CountGroupUsers :many
-- How many clients each group holds, hidden ones too: n of their own and of their tariff's
-- (own: those of them whose group is their own).
SELECT COALESCE(m.group_id, t.group_id)::bigint AS group_id, count(*) AS n, count(m.group_id) AS own
FROM users u
LEFT JOIN user_group_members m ON m.user_id = u.id
LEFT JOIN user_group_tariffs t ON t.tariff_id = u.tariff_id
WHERE COALESCE(m.group_id, t.group_id) IS NOT NULL
GROUP BY 1;

-- name: CountGroupTariffs :many
-- How many tariffs, archived ones aside, put their clients into each group.
SELECT g.group_id, count(*) AS n FROM user_group_tariffs g JOIN tariffs t ON t.id = g.tariff_id
WHERE t.archived = 0 GROUP BY g.group_id;

-- name: GetUserGroup :one
SELECT group_id FROM user_group_members WHERE user_id = $1;

-- name: SetUserGroup :exec
INSERT INTO user_group_members (user_id, group_id) VALUES ($1, $2)
ON CONFLICT (user_id) DO UPDATE SET group_id = excluded.group_id;

-- name: ClearUserGroup :exec
-- The client goes back to its tariff's group.
DELETE FROM user_group_members WHERE user_id = $1;

-- name: SetUsersGroup :exec
INSERT INTO user_group_members (user_id, group_id)
SELECT unnest(sqlc.arg(ids)::bigint[]), sqlc.arg(group_id)::bigint
ON CONFLICT (user_id) DO UPDATE SET group_id = excluded.group_id;

-- name: ClearUsersGroup :exec
DELETE FROM user_group_members WHERE user_id = ANY(sqlc.arg(ids)::bigint[]);

-- name: ListUserGroupIDs :many
-- The group of every client that has one of its own.
SELECT user_id, group_id FROM user_group_members;

-- name: ListUserGroupIDsOf :many
SELECT user_id, group_id FROM user_group_members WHERE user_id = ANY(sqlc.arg(ids)::bigint[]);

-- name: GetTariffGroup :one
SELECT group_id FROM user_group_tariffs WHERE tariff_id = $1;

-- name: SetTariffGroup :exec
INSERT INTO user_group_tariffs (tariff_id, group_id) VALUES ($1, $2)
ON CONFLICT (tariff_id) DO UPDATE SET group_id = excluded.group_id;

-- name: ClearTariffGroup :exec
DELETE FROM user_group_tariffs WHERE tariff_id = $1;

-- name: ListTariffGroupIDs :many
-- The group of every tariff that has one, archived ones too: their clients keep it to the
-- end of their term.
SELECT tariff_id, group_id FROM user_group_tariffs;

-- name: SetGroupChosenServers :exec
UPDATE user_groups SET chosen_servers = $2 WHERE id = $1;

-- name: ClearGroupNodes :exec
DELETE FROM user_group_nodes WHERE group_id = $1;

-- name: ClearGroupInbounds :exec
DELETE FROM user_group_inbounds WHERE group_id = $1;

-- name: AddGroupNode :exec
INSERT INTO user_group_nodes (group_id, node_id) VALUES ($1, $2) ON CONFLICT DO NOTHING;

-- name: AddGroupInbound :exec
INSERT INTO user_group_inbounds (group_id, inbound_id) VALUES ($1, $2) ON CONFLICT DO NOTHING;

-- name: ListGroupNodes :many
SELECT node_id FROM user_group_nodes WHERE group_id = $1 ORDER BY node_id;

-- name: ListGroupInbounds :many
SELECT inbound_id FROM user_group_inbounds WHERE group_id = $1 ORDER BY inbound_id;

-- name: ListAllGroupNodes :many
SELECT * FROM user_group_nodes ORDER BY group_id, node_id;

-- name: ListAllGroupInbounds :many
SELECT * FROM user_group_inbounds ORDER BY group_id, inbound_id;

-- name: ListGroupedHosts :many
-- The addresses for clients shown or kept by group, with their inbounds' names: what a
-- group's page lists of them.
SELECT h.id, h.inbound_id, h.position, h.address, h.name, h.group_mode, h.groups, i.node_id, i.name AS inbound_name
FROM inbound_hosts h JOIN inbounds i ON i.id = h.inbound_id
WHERE h.group_mode <> ''
ORDER BY i.node_id, h.inbound_id, h.position;

-- name: RemoveGroupFromHosts :execrows
-- A deleted group leaves the addresses that named it; one that was "never" for it alone goes
-- back to every client.
UPDATE inbound_hosts SET groups = array_remove(groups, sqlc.arg(group_id)::bigint),
  group_mode = CASE WHEN group_mode = 'never' AND groups = ARRAY[sqlc.arg(group_id)::bigint] THEN '' ELSE group_mode END
WHERE sqlc.arg(group_id)::bigint = ANY(groups);
