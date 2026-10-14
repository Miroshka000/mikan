-- name: CreateCampaignTag :one
INSERT INTO campaign_tags (tag, name, note, created_at, updated_at)
VALUES ($1, $2, $3, $4, $4) RETURNING *;

-- name: GetCampaignTag :one
SELECT * FROM campaign_tags WHERE id = $1;

-- name: GetCampaignTagByTag :one
SELECT * FROM campaign_tags WHERE tag = $1;

-- name: UpdateCampaignTag :execrows
UPDATE campaign_tags SET name = sqlc.arg(name), note = sqlc.arg(note), enabled = sqlc.arg(enabled), updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id) AND deleted = 0;

-- name: SetCampaignTagEnabled :execrows
UPDATE campaign_tags SET enabled = sqlc.arg(enabled), updated_at = sqlc.arg(updated_at) WHERE id = sqlc.arg(id) AND deleted = 0;

-- name: DeleteCampaignTag :execrows
UPDATE campaign_tags SET deleted = 1, updated_at = sqlc.arg(updated_at) WHERE id = sqlc.arg(id) AND deleted = 0;

-- name: ListCampaignTags :many
SELECT * FROM campaign_tags WHERE deleted = 0 ORDER BY id DESC LIMIT CAST(sqlc.arg(lim) AS BIGINT) OFFSET CAST(sqlc.arg(row_offset) AS BIGINT);

-- name: CountCampaignTags :one
SELECT COUNT(*) AS total FROM campaign_tags WHERE deleted = 0;

-- name: TakeCampaignVisit :execrows
-- A chat is bound to the campaign of its first tagged link for good: a second link
-- changes nothing, so an arrival is never counted twice.
INSERT INTO campaign_visits (campaign_id, chat_id, tag, first_seen)
VALUES ($1, $2, $3, $4) ON CONFLICT (chat_id) DO NOTHING;

-- name: GetCampaignVisit :one
SELECT * FROM campaign_visits WHERE chat_id = $1;

-- name: CountCampaignVisits :one
SELECT COUNT(*) AS visits, CAST(COALESCE(MIN(first_seen), 0) AS BIGINT) AS first_seen
FROM campaign_visits WHERE campaign_id = $1;

-- name: CreateCampaignEvent :one
INSERT INTO campaign_events (campaign_id, chat_id, payment_id, kind, amount, currency, created_at)
VALUES (sqlc.arg(campaign_id), sqlc.arg(chat_id), sqlc.narg(payment_id), sqlc.arg(kind), sqlc.arg(amount), sqlc.arg(currency), sqlc.arg(created_at))
RETURNING *;

-- name: ListCampaignEventsByPayment :many
SELECT * FROM campaign_events WHERE payment_id = $1;

-- name: RevertCampaignEventsByPayment :execrows
UPDATE campaign_events SET reverted_at = sqlc.arg(reverted_at) WHERE payment_id = sqlc.arg(payment_id) AND reverted_at IS NULL;

-- name: CountCampaignEvents :one
-- What a campaign brought: arrivals, first purchases and renewals, with the money of the
-- payments that stand (a refunded one is reverted and left out).
SELECT
  CAST(COUNT(*) FILTER (WHERE kind = 'bought' AND reverted_at IS NULL) AS BIGINT) AS bought,
  CAST(COUNT(*) FILTER (WHERE kind = 'renewed' AND reverted_at IS NULL) AS BIGINT) AS renewed,
  CAST(COALESCE(SUM(CASE WHEN reverted_at IS NULL AND currency = 'RUB' THEN amount ELSE 0 END), 0) AS BIGINT) AS rub
FROM campaign_events WHERE campaign_id = $1;

-- name: ListCampaignTagsWithStats :many
-- Every campaign with its arrivals and its sales, for the admin's page: arrival comes from
-- campaign_visits (a chat that followed the link), the rest from applied payments.
SELECT c.*,
  CAST(COALESCE((SELECT COUNT(*) FROM campaign_visits v WHERE v.campaign_id = c.id), 0) AS BIGINT) AS visits,
  CAST(COALESCE((SELECT COUNT(*) FROM campaign_events e WHERE e.campaign_id = c.id AND e.kind = 'bought' AND e.reverted_at IS NULL), 0) AS BIGINT) AS bought,
  CAST(COALESCE((SELECT COUNT(*) FROM campaign_events e WHERE e.campaign_id = c.id AND e.kind = 'renewed' AND e.reverted_at IS NULL), 0) AS BIGINT) AS renewed,
  CAST(COALESCE((SELECT SUM(e.amount) FROM campaign_events e WHERE e.campaign_id = c.id AND e.reverted_at IS NULL AND e.currency = 'RUB'), 0) AS BIGINT) AS rub
FROM campaign_tags c
WHERE c.deleted = 0
ORDER BY c.id DESC LIMIT CAST(sqlc.arg(lim) AS BIGINT) OFFSET CAST(sqlc.arg(row_offset) AS BIGINT);

-- name: ListCampaignVisits :many
SELECT v.*, CAST(COALESCE(tc.username, '') AS TEXT) AS username, CAST(COALESCE(tc.first_name, '') AS TEXT) AS first_name
FROM campaign_visits v LEFT JOIN tg_chats tc ON tc.tg_id = v.chat_id
WHERE v.campaign_id = $1
ORDER BY v.id DESC LIMIT CAST(sqlc.arg(lim) AS BIGINT) OFFSET CAST(sqlc.arg(row_offset) AS BIGINT);

-- name: CountCampaignVisitsOf :one
SELECT COUNT(*) AS total FROM campaign_visits WHERE campaign_id = $1;
