-- The lifecycle of a subscription past its plain term: a tariff's own rules, a term that
-- waits for the first connection, the next tariff, and who is inactive enough to remove.

-- name: GetTariffLifecycle :one
SELECT * FROM tariff_lifecycle WHERE tariff_id = $1;

-- name: ListTariffLifecycles :many
SELECT * FROM tariff_lifecycle ORDER BY tariff_id;

-- name: SetTariffLifecycle :exec
INSERT INTO tariff_lifecycle (tariff_id, carry_over, start_on_connect) VALUES ($1, $2, $3)
ON CONFLICT (tariff_id) DO UPDATE SET carry_over = excluded.carry_over, start_on_connect = excluded.start_on_connect;

-- name: AddUserStart :exec
INSERT INTO user_starts (user_id, frozen_at) VALUES ($1, $2) ON CONFLICT (user_id) DO NOTHING;

-- name: GetUserStart :one
SELECT * FROM user_starts WHERE user_id = $1;

-- name: ListUserStarts :many
SELECT * FROM user_starts ORDER BY user_id;

-- name: ListUserStartsOf :many
SELECT * FROM user_starts WHERE user_id = ANY(sqlc.arg(ids)::bigint[]) ORDER BY user_id;

-- name: TakeUserStarts :many
-- The waits of these users end: what is returned is when each clock stood still last.
DELETE FROM user_starts WHERE user_id = ANY(sqlc.arg(ids)::bigint[]) RETURNING user_id, frozen_at;

-- name: FreezeUserStarts :exec
UPDATE user_starts SET frozen_at = sqlc.arg(now)::bigint WHERE user_id = ANY(sqlc.arg(ids)::bigint[]);

-- name: SlideUserTerms :exec
-- Moves the clock of each waiting user on by its own span in seconds: the expiry, a traffic
-- period counted from its start (reset strategy period), and the places bought until the end
-- of the term. An expiry stops at the last time the panel keeps; none stays none.
UPDATE users u SET
  expires_at = CASE WHEN u.expires_at IS NULL THEN NULL ELSE LEAST(u.expires_at + t.span, 253402300799) END,
  period_start = CASE WHEN u.reset_strategy = 'period' THEN u.period_start + t.span ELSE u.period_start END,
  extra_devices_until = CASE WHEN u.extra_devices_until IS NULL THEN NULL ELSE u.extra_devices_until + t.span END
FROM (SELECT unnest(sqlc.arg(ids)::bigint[]) AS id, unnest(sqlc.arg(spans)::bigint[]) AS span) AS t
WHERE u.id = t.id;

-- name: SlideUserGrants :exec
-- The packages of each waiting user that were still running when its clock stood still last
-- (frozen) end later by its span.
UPDATE traffic_grants g SET expires_at = g.expires_at + t.span
FROM (SELECT unnest(sqlc.arg(ids)::bigint[]) AS user_id, unnest(sqlc.arg(spans)::bigint[]) AS span,
             unnest(sqlc.arg(frozen)::bigint[]) AS frozen_at) AS t
WHERE g.user_id = t.user_id AND g.remaining > 0 AND g.expires_at IS NOT NULL AND g.expires_at > t.frozen_at;

-- name: GetNextTariff :one
SELECT * FROM user_next_tariffs WHERE user_id = $1;

-- name: ListNextTariffsOf :many
SELECT * FROM user_next_tariffs WHERE user_id = ANY(sqlc.arg(ids)::bigint[]) ORDER BY user_id;

-- name: SetNextTariff :exec
INSERT INTO user_next_tariffs (user_id, tariff_id, term_days, created_at) VALUES ($1, $2, $3, $4)
ON CONFLICT (user_id) DO UPDATE SET tariff_id = excluded.tariff_id, term_days = excluded.term_days, created_at = excluded.created_at;

-- name: DeleteNextTariff :execrows
DELETE FROM user_next_tariffs WHERE user_id = $1;

-- name: ListDueNextTariffs :many
-- The subscriptions with a next tariff whose term ends before `before`. A term that waits
-- for its first connection does not end.
SELECT n.user_id FROM user_next_tariffs n JOIN users u ON u.id = n.user_id
WHERE u.expires_at IS NOT NULL AND u.expires_at <= sqlc.arg(before)::bigint
  AND NOT EXISTS (SELECT 1 FROM user_starts s WHERE s.user_id = n.user_id)
ORDER BY n.user_id;

-- name: ListInactiveUsers :many
-- The subscriptions whose term ended and whose last connection was both at or before
-- `before` (now less the removal rules' days), oldest first, with what keeps each of them:
-- money on the linked account's balance, a payment not applied yet or a top-up waiting for
-- the admin, traffic packages still running. A term that waits for its first connection
-- has not ended, and one a next tariff follows is not over. user_id 0 lists every one; any
-- other the one alone, as the removal's own transaction checks it again.
SELECT u.id, u.name, u.tariff_id, u.status, CAST(u.expires_at AS BIGINT) AS expires_at, u.online_at,
       CAST(COALESCE(l.tg_id, 0) AS BIGINT) AS tg_id,
       CAST(COALESCE(b.amount, 0) > 0 AS BOOLEAN) AS has_balance,
       EXISTS (SELECT 1 FROM payments p WHERE p.user_id = u.id AND p.status IN ('pending', 'paid')) AS paying,
       CAST(l.tg_id IS NOT NULL AND EXISTS (SELECT 1 FROM topups t WHERE t.tg_id = l.tg_id AND t.status = 'pending') AS BOOLEAN) AS topping_up,
       EXISTS (SELECT 1 FROM traffic_grants g WHERE g.user_id = u.id AND g.remaining > 0
               AND (g.expires_at IS NULL OR g.expires_at > sqlc.arg(now)::bigint)) AS granted
FROM users u
LEFT JOIN tg_links l ON l.user_id = u.id
LEFT JOIN balances b ON b.tg_id = l.tg_id
WHERE u.expires_at IS NOT NULL AND u.expires_at <= sqlc.arg(before)::bigint
  AND COALESCE(u.online_at, 0) <= sqlc.arg(before)::bigint
  AND NOT EXISTS (SELECT 1 FROM user_starts s WHERE s.user_id = u.id)
  AND NOT EXISTS (SELECT 1 FROM user_next_tariffs n WHERE n.user_id = u.id)
  AND (sqlc.arg(user_id)::bigint = 0 OR u.id = sqlc.arg(user_id)::bigint)
ORDER BY u.expires_at, u.id;

-- name: CountUsersAtStake :one
-- Of these users, how many hold what deleting them takes along, as the removal of inactive
-- ones counts it: money on the linked account's balance or on its way there, a payment not
-- applied yet, traffic packages still running.
SELECT
  count(*) FILTER (WHERE EXISTS (SELECT 1 FROM tg_links l JOIN balances b ON b.tg_id = l.tg_id WHERE l.user_id = u.id AND b.amount > 0)) AS with_balance,
  count(*) FILTER (WHERE EXISTS (SELECT 1 FROM payments p WHERE p.user_id = u.id AND p.status IN ('pending', 'paid'))
                      OR EXISTS (SELECT 1 FROM tg_links l JOIN topups t ON t.tg_id = l.tg_id WHERE l.user_id = u.id AND t.status = 'pending')) AS paying,
  count(*) FILTER (WHERE EXISTS (SELECT 1 FROM traffic_grants g WHERE g.user_id = u.id AND g.remaining > 0
                                 AND (g.expires_at IS NULL OR g.expires_at > sqlc.arg(now)::bigint))) AS granted
FROM users u WHERE u.id = ANY(sqlc.arg(ids)::bigint[]);
