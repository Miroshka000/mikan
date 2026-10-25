-- name: CreateWebhook :one
-- A new endpoint starts at the end of the feed: it is told what happens from now on, not what
-- the feed still holds of the week before.
INSERT INTO webhook_endpoints (name, url, secret, events, enabled, allow_local, last_id, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, (SELECT COALESCE(max(id), 0) FROM addon_events), $7, $7)
RETURNING *;

-- name: GetWebhook :one
SELECT * FROM webhook_endpoints WHERE id = $1;

-- name: ListWebhooks :many
SELECT * FROM webhook_endpoints ORDER BY id;

-- name: CountWebhooks :one
SELECT count(*) FROM webhook_endpoints;

-- name: UpdateWebhook :one
UPDATE webhook_endpoints
SET name = $2, url = $3, events = $4, enabled = $5, allow_local = $6, updated_at = $7
WHERE id = $1
RETURNING *;

-- name: SetWebhookSecret :one
UPDATE webhook_endpoints SET secret = $2, updated_at = $3 WHERE id = $1
RETURNING *;

-- name: DeleteWebhook :execrows
DELETE FROM webhook_endpoints WHERE id = $1;

-- name: AckWebhook :exec
-- The cursor only moves forward: a late ack of an older event does not send the rest again.
-- updated_at is left alone, it is the admin's edits that cut a worker's pause short.
UPDATE webhook_endpoints SET last_id = GREATEST(last_id, sqlc.arg(last_id)::BIGINT) WHERE id = sqlc.arg(id);

-- name: SkipWebhookHistory :exec
-- For an endpoint about to be told of more types: when nothing waits for it under the choice
-- it has now, the cursor goes to the end of the feed, so the types added start from now and
-- the week's old events of them are not sent. What waits stays: it is still owed.
UPDATE webhook_endpoints SET last_id = GREATEST(last_id, (SELECT COALESCE(max(id), 0) FROM addon_events))
WHERE webhook_endpoints.id = $1 AND NOT EXISTS (
  SELECT 1 FROM addon_events e
  WHERE e.id > webhook_endpoints.last_id AND (cardinality(webhook_endpoints.events) = 0 OR e.type = ANY(webhook_endpoints.events))
);

-- name: WebhookBacklog :many
-- How many events wait for each endpoint: after its cursor, of the types it is told of.
SELECT w.id AS endpoint_id, count(e.id)::BIGINT AS waiting
FROM webhook_endpoints w
LEFT JOIN addon_events e ON e.id > w.last_id AND (cardinality(w.events) = 0 OR e.type = ANY(w.events))
GROUP BY w.id;

-- name: RecordWebhookAttempt :exec
-- The first attempt at an event makes its row, each one after updates it. A test event has no
-- event_id, and every one of them is a row of its own.
INSERT INTO webhook_deliveries (endpoint_id, event_id, type, status, attempts, response_code, error, created_at, updated_at)
VALUES ($1, $2, $3, $4, 1, $5, $6, $7, $7)
ON CONFLICT (endpoint_id, event_id) DO UPDATE
SET status = EXCLUDED.status, attempts = webhook_deliveries.attempts + 1, response_code = EXCLUDED.response_code,
    error = EXCLUDED.error, updated_at = EXCLUDED.updated_at;

-- name: ListWebhookDeliveries :many
-- The log of one endpoint, newest first; status "" is every status.
SELECT * FROM webhook_deliveries
WHERE endpoint_id = sqlc.arg(endpoint_id) AND id < sqlc.arg(before)::BIGINT
  AND (sqlc.arg(status)::TEXT = '' OR status = sqlc.arg(status)::TEXT)
ORDER BY id DESC
LIMIT sqlc.arg(lim)::BIGINT;

-- name: LastWebhookDeliveries :many
-- The newest delivery of every endpoint: what the list shows of how it fares.
SELECT DISTINCT ON (endpoint_id) * FROM webhook_deliveries ORDER BY endpoint_id, id DESC;

-- name: FailWebhookRetriesOutside :execrows
-- An endpoint that no longer asks for a type is not sent the event of it that was being tried.
UPDATE webhook_deliveries SET status = 'failed'
WHERE endpoint_id = sqlc.arg(endpoint_id) AND status = 'retrying'
  AND cardinality(sqlc.arg(types)::TEXT[]) > 0 AND NOT type = ANY(sqlc.arg(types)::TEXT[]);

-- name: FailLostWebhookDeliveries :execrows
-- An event that was still being tried but left the feed (it keeps a week) is never sent.
UPDATE webhook_deliveries SET status = 'failed'
WHERE status = 'retrying' AND event_id IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM addon_events WHERE addon_events.id = webhook_deliveries.event_id);

-- name: PruneWebhookDeliveries :execrows
DELETE FROM webhook_deliveries WHERE updated_at < sqlc.arg(before)::BIGINT;
