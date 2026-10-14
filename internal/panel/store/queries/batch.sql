-- Set-based statements: one statement for many rows instead of one per row. Rows that
-- other writers change too are locked in id order first, so two writers never wait for
-- each other in a circle.

-- name: LockUsers :many
-- The users of ids that exist, locked in id order until the transaction ends.
SELECT id FROM users WHERE id = ANY(sqlc.arg(ids)::bigint[]) ORDER BY id FOR NO KEY UPDATE;

-- name: SlotOwners :many
-- The users of the named slots: the own slot and those of bound devices.
SELECT s.name AS slot_name, u.id AS user_id FROM slots s JOIN users u ON u.slot_id = s.id
WHERE s.name = ANY(sqlc.arg(names)::text[])
UNION
SELECT s.name AS slot_name, d.user_id AS user_id FROM slots s JOIN bound_devices d ON d.slot_id = s.id
WHERE s.name = ANY(sqlc.arg(names)::text[]);

-- name: LockTrafficPools :many
-- The pools of ids that exist; KEY SHARE keeps them from being deleted until the
-- transaction ends, so the pool counters written after it cannot fail on one.
SELECT id FROM traffic_pools WHERE id = ANY(sqlc.arg(ids)::bigint[]) ORDER BY id FOR KEY SHARE;

-- name: AddUsersTraffic :many
-- A traffic batch by user: main traffic counts to the period and the totals, pool traffic
-- to the totals only. It returns the period's counters with the batch in them.
UPDATE users u
SET used_up = u.used_up + t.up, used_down = u.used_down + t.down,
    total_up = u.total_up + t.up + t.pool_up, total_down = u.total_down + t.down + t.pool_down
FROM (SELECT unnest(sqlc.arg(ids)::bigint[]) AS id, unnest(sqlc.arg(up)::bigint[]) AS up, unnest(sqlc.arg(down)::bigint[]) AS down,
             unnest(sqlc.arg(pool_up)::bigint[]) AS pool_up, unnest(sqlc.arg(pool_down)::bigint[]) AS pool_down) AS t
WHERE u.id = t.id
RETURNING u.id, u.traffic_limit, CAST(u.used_up + u.used_down AS BIGINT) AS used;

-- name: AddUserPoolsTraffic :many
-- A batch of pool traffic by user and pool; it returns the pools' counters with it.
INSERT INTO user_pools (user_id, pool_id, used_up, used_down)
SELECT unnest(sqlc.arg(user_ids)::bigint[]), unnest(sqlc.arg(pool_ids)::bigint[]), unnest(sqlc.arg(up)::bigint[]), unnest(sqlc.arg(down)::bigint[])
ORDER BY 1, 2
ON CONFLICT (user_id, pool_id) DO UPDATE SET used_up = user_pools.used_up + excluded.used_up, used_down = user_pools.used_down + excluded.used_down
RETURNING user_id, pool_id, traffic_limit, CAST(used_up + used_down AS BIGINT) AS used;

-- name: LockSpendableGrants :many
-- The active grants of these users in the spending order of each target (pool 0: the
-- main traffic), locked: traffic past a base quota is taken from them.
SELECT id, user_id, CAST(COALESCE(pool_id, 0) AS BIGINT) AS pool_id, remaining FROM traffic_grants
WHERE user_id = ANY(sqlc.arg(user_ids)::bigint[]) AND remaining > 0
  AND (expires_at IS NULL OR expires_at > CAST(sqlc.arg(now) AS BIGINT))
ORDER BY user_id, COALESCE(pool_id, 0), expires_at IS NULL, expires_at, created_at, id
FOR NO KEY UPDATE;

-- name: SpendGrants :exec
UPDATE traffic_grants g SET remaining = g.remaining - LEAST(g.remaining, t.spent)
FROM (SELECT unnest(sqlc.arg(ids)::bigint[]) AS id, unnest(sqlc.arg(spent)::bigint[]) AS spent) AS t
WHERE g.id = t.id;

-- name: AddTrafficHourlyBatch :exec
INSERT INTO traffic_hourly (user_id, hour, up, down)
SELECT unnest(sqlc.arg(user_ids)::bigint[]), sqlc.arg(hour)::bigint, unnest(sqlc.arg(up)::bigint[]), unnest(sqlc.arg(down)::bigint[])
ORDER BY 1
ON CONFLICT (user_id, hour) DO UPDATE SET up = traffic_hourly.up + excluded.up, down = traffic_hourly.down + excluded.down;

-- name: AddTrafficDailyBatch :exec
INSERT INTO traffic_daily (user_id, day, up, down)
SELECT unnest(sqlc.arg(user_ids)::bigint[]), sqlc.arg(day)::bigint, unnest(sqlc.arg(up)::bigint[]), unnest(sqlc.arg(down)::bigint[])
ORDER BY 1
ON CONFLICT (user_id, day) DO UPDATE SET up = traffic_daily.up + excluded.up, down = traffic_daily.down + excluded.down;

-- name: SetUsersOnline :exec
UPDATE users SET online_at = sqlc.arg(online_at) WHERE id = ANY(sqlc.arg(ids)::bigint[]);

-- name: UpsertDevices :exec
-- Devices seen online. A user deleted meanwhile is skipped: the rest still goes in.
INSERT INTO devices (user_id, ip, first_seen, last_seen)
SELECT t.user_id, t.ip, sqlc.arg(now)::bigint, sqlc.arg(now)::bigint
FROM (SELECT unnest(sqlc.arg(user_ids)::bigint[]) AS user_id, unnest(sqlc.arg(ips)::text[]) AS ip) AS t
JOIN users u ON u.id = t.user_id
ORDER BY t.user_id, t.ip
ON CONFLICT (user_id, ip) DO UPDATE SET last_seen = excluded.last_seen;

-- name: StartPeriodIfOlder :execrows
-- A new traffic period over an older one only: a payment that started one meanwhile is
-- not reset again.
UPDATE users SET used_up = 0, used_down = 0, period_start = sqlc.arg(period_start), updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id) AND period_start < sqlc.arg(period_start);

-- name: ReserveSlotNumbers :one
-- Moves the slot counter on by n and returns the last number reserved. Names go on from
-- the last number ever handed out (the largest id for a counter that was never kept);
-- concurrent refills wait on the counter's row and never get the same numbers.
INSERT INTO slot_counter (id, last) VALUES (1, (SELECT coalesce(max(id), 0) FROM slots) + sqlc.arg(n)::bigint)
ON CONFLICT (id) DO UPDATE SET last = GREATEST(slot_counter.last, (SELECT coalesce(max(id), 0) FROM slots)) + sqlc.arg(n)::bigint
RETURNING last;

-- name: InsertSlots :exec
INSERT INTO slots (name, uuid, secret, state, created_at)
SELECT unnest(sqlc.arg(names)::text[]), unnest(sqlc.arg(uuids)::text[]), unnest(sqlc.arg(secrets)::text[]), 'free', sqlc.arg(created_at)::bigint;

-- name: LockUserRows :many
-- The users of ids that exist, locked in id order for a change.
SELECT * FROM users WHERE id = ANY(sqlc.arg(ids)::bigint[]) ORDER BY id FOR NO KEY UPDATE;

-- name: LockUserRowsForDelete :many
SELECT * FROM users WHERE id = ANY(sqlc.arg(ids)::bigint[]) ORDER BY id FOR UPDATE;

-- name: SetUsersStatus :exec
UPDATE users SET status = sqlc.arg(status), updated_at = sqlc.arg(updated_at) WHERE id = ANY(sqlc.arg(ids)::bigint[]);

-- name: SetUsersExpiry :exec
-- A new term for each user; it also turns the user on.
UPDATE users u SET expires_at = t.expires_at, status = 'active', updated_at = sqlc.arg(updated_at)
FROM (SELECT unnest(sqlc.arg(ids)::bigint[]) AS id, unnest(sqlc.arg(expires_at)::bigint[]) AS expires_at) AS t
WHERE u.id = t.id;

-- name: ResetUsersTraffic :exec
UPDATE users SET used_up = 0, used_down = 0, period_start = sqlc.arg(now), updated_at = sqlc.arg(now)
WHERE id = ANY(sqlc.arg(ids)::bigint[]);

-- name: ResetUsersPools :exec
UPDATE user_pools SET used_up = 0, used_down = 0 WHERE user_id = ANY(sqlc.arg(ids)::bigint[]);

-- name: EndUsersPeriodGrants :exec
UPDATE traffic_grants SET expires_at = CAST(sqlc.arg(now) AS BIGINT)
WHERE user_id = ANY(sqlc.arg(ids)::bigint[]) AND lifetime = 'period' AND remaining > 0
  AND (expires_at IS NULL OR expires_at > CAST(sqlc.arg(now) AS BIGINT));

-- name: BurnUsersSlots :exec
-- The slots of these users and of their registered bound devices stop working.
UPDATE slots SET state = 'burned', burned_at = sqlc.arg(burned_at)
WHERE id IN (SELECT slot_id FROM users WHERE id = ANY(sqlc.arg(ids)::bigint[]) AND slot_id IS NOT NULL
             UNION SELECT slot_id FROM bound_devices WHERE user_id = ANY(sqlc.arg(ids)::bigint[]) AND hwid <> '');

-- name: DeleteUsersBoundDevices :exec
DELETE FROM bound_devices WHERE user_id = ANY(sqlc.arg(ids)::bigint[]);

-- name: DeleteUsers :exec
DELETE FROM users WHERE id = ANY(sqlc.arg(ids)::bigint[]);

-- name: SumUserTraffic :one
-- Traffic of all users, all time.
SELECT COALESCE(SUM(total_up), 0)::bigint AS up, COALESCE(SUM(total_down), 0)::bigint AS down FROM users;

-- name: CountUserStates :one
-- How many users are in each state as domain.State decides it (keep the two alike):
-- disabled, expired, limited (past the base quota with no main grants left), expiring
-- (the term ends within expiring_within seconds), otherwise active.
WITH g AS (
  SELECT user_id, SUM(remaining) AS left_bytes FROM traffic_grants
  WHERE pool_id IS NULL AND remaining > 0 AND (expires_at IS NULL OR expires_at > sqlc.arg(now)::bigint)
  GROUP BY user_id
), s AS (
  SELECT CASE
    WHEN u.status = 'disabled' THEN 'disabled'
    WHEN u.expires_at IS NOT NULL AND u.expires_at <= sqlc.arg(now)::bigint THEN 'expired'
    WHEN u.traffic_limit IS NOT NULL AND u.used_up + u.used_down >= u.traffic_limit AND COALESCE(g.left_bytes, 0) <= 0 THEN 'limited'
    WHEN u.expires_at IS NOT NULL AND u.expires_at - sqlc.arg(now)::bigint <= sqlc.arg(expiring_within)::bigint THEN 'expiring'
    ELSE 'active' END AS state
  FROM users u LEFT JOIN g ON g.user_id = u.id
)
SELECT count(*) AS total,
  count(*) FILTER (WHERE state = 'active') AS active,
  count(*) FILTER (WHERE state = 'expiring') AS expiring,
  count(*) FILTER (WHERE state = 'limited') AS limited,
  count(*) FILTER (WHERE state = 'expired') AS expired,
  count(*) FILTER (WHERE state = 'disabled') AS disabled
FROM s;

-- name: UserSlotsOf :many
-- The slots of these users: the own one and those of bound devices.
SELECT s.name AS slot_name, u.id AS user_id FROM slots s JOIN users u ON u.slot_id = s.id
WHERE u.id = ANY(sqlc.arg(ids)::bigint[])
UNION
SELECT s.name AS slot_name, d.user_id AS user_id FROM slots s JOIN bound_devices d ON d.slot_id = s.id
WHERE d.user_id = ANY(sqlc.arg(ids)::bigint[]);

-- name: CountersPositions :many
-- Every node's counters position: counters_epoch/<node> and counters_seq/<node>.
SELECT key, value FROM node_state WHERE key LIKE 'counters\_epoch/%' OR key LIKE 'counters\_seq/%';

-- name: ListNoticeSubscriptions :many
-- The subscriptions the bot may tell about (linked, the chat not blocked) with what is
-- left of their main grants: one read for a round of notices.
SELECT sqlc.embed(users), l.tg_id, CAST(COALESCE(g.left_bytes, 0) AS BIGINT) AS grants_left
FROM tg_links l
JOIN users ON users.id = l.user_id
LEFT JOIN tg_chats c ON c.tg_id = l.tg_id
LEFT JOIN (SELECT user_id, SUM(remaining) AS left_bytes FROM traffic_grants
           WHERE pool_id IS NULL AND remaining > 0 AND (expires_at IS NULL OR expires_at > sqlc.arg(now)::bigint)
           GROUP BY user_id) g ON g.user_id = users.id
WHERE COALESCE(c.blocked, 0) = 0
ORDER BY users.id;

-- name: TgNoticeSent :one
SELECT EXISTS (SELECT 1 FROM tg_notices WHERE user_id = $1 AND kind = $2 AND period = $3);

-- name: InboundEventsAfter :many
-- The automatic changes after a cursor, oldest first, a page at a time.
SELECT * FROM inbound_events WHERE id > $1 ORDER BY id LIMIT 500;

-- name: LockBuyerInvoices :exec
-- The invoices of one Telegram account are checked and made one transaction at a time.
SELECT pg_advisory_xact_lock(hashtextextended('mikan-invoice:' || CAST(sqlc.arg(tg_id) AS BIGINT), 0));

-- name: CountBoundDevicesOf :many
-- How many devices are bound to each of these users: the places they take.
SELECT user_id, count(*) AS n FROM bound_devices WHERE user_id = ANY(sqlc.arg(ids)::bigint[]) GROUP BY user_id;
