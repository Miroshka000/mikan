-- name: CreatePayment :one
INSERT INTO payments (provider, payload, tg_id, kind, user_id, tariff_id, tariff_name, amount, currency, status, created_at, term_days)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'pending', $10, $11)
RETURNING *;

-- name: GetPayment :one
SELECT * FROM payments WHERE id = $1;

-- name: GetPaymentByPayload :one
SELECT * FROM payments WHERE payload = $1;

-- name: GetPaymentByExternal :one
SELECT * FROM payments WHERE provider = $1 AND external_id = $2;

-- name: SetPaymentInvoice :exec
UPDATE payments SET external_id = $1, pay_url = $2 WHERE id = $3;

-- name: SetPaymentExternalID :exec
UPDATE payments SET external_id = sqlc.arg(external_id) WHERE id = sqlc.arg(id);

-- name: SetPaymentAmount :execrows
UPDATE payments SET amount = sqlc.arg(amount) WHERE id = sqlc.arg(id) AND status = 'pending';

-- name: MarkPaymentPaid :execrows
UPDATE payments SET status = 'paid', external_id = $1, paid_at = $2
WHERE id = $3 AND status IN ('pending', 'expired');

-- name: MarkPaymentApplied :execrows
UPDATE payments SET status = 'applied', user_id = $1, applied_at = $2, error = ''
WHERE id = $3 AND status = 'paid';

-- name: SetPaymentError :exec
UPDATE payments SET error = $1 WHERE id = $2;

-- name: SetPaymentStatus :execrows
UPDATE payments SET status = sqlc.arg(new_status) WHERE id = sqlc.arg(id) AND status = sqlc.arg(old_status);

-- name: MarkPaymentRefunded :execrows
UPDATE payments SET status = 'refunded', refunded_at = $1 WHERE id = $2 AND status = 'applied';

-- name: MarkLatePromoRefunded :execrows
UPDATE payments SET status = 'refunded', refunded_at = sqlc.arg(refunded_at), error = '' WHERE id = sqlc.arg(id) AND status IN ('pending','expired','paid','failed');

-- name: FindOpenPayment :one
-- user_id 0 asks for an invoice that has no user yet (NULL); payments_tg finds the candidates.
SELECT * FROM payments
WHERE tg_id = $1 AND tariff_id = $2 AND provider = $3 AND kind = $4 AND user_id IS NOT DISTINCT FROM NULLIF(CAST(sqlc.arg(user_id) AS BIGINT), 0)
  AND status = 'pending' AND pay_url <> '' AND created_at > sqlc.arg(since)
ORDER BY id DESC LIMIT 1;

-- name: FindOpenPayments :many
SELECT * FROM payments
WHERE tg_id = sqlc.arg(tg_id) AND tariff_id = sqlc.arg(tariff_id) AND provider = sqlc.arg(provider) AND kind = sqlc.arg(kind)
  AND user_id IS NOT DISTINCT FROM NULLIF(CAST(sqlc.arg(user_id) AS BIGINT), 0)
  AND status = 'pending' AND pay_url <> '' AND created_at > sqlc.arg(since)
ORDER BY id DESC;

-- name: ListPendingPayments :many
SELECT * FROM payments WHERE status = 'pending' AND created_at > $1 ORDER BY id;

-- name: ListPaidPayments :many
SELECT * FROM payments WHERE status = 'paid' ORDER BY id;

-- name: CountRecentInvoices :one
SELECT count(*) FROM payments WHERE tg_id = $1 AND status IN ('pending', 'paid') AND created_at > $2;

-- name: ExpirePayments :execrows
UPDATE payments SET status = 'expired' WHERE status = 'pending' AND created_at < $1;

-- name: ListPayments :many
SELECT sqlc.embed(payments), CAST(COALESCE(users.name, '') AS TEXT) AS user_name, CAST(COALESCE(tg_chats.username, '') AS TEXT) AS tg_username
FROM payments
LEFT JOIN users ON users.id = payments.user_id
LEFT JOIN tg_chats ON tg_chats.tg_id = payments.tg_id
WHERE payments.id < sqlc.arg(before_id)
  AND (CAST(sqlc.arg(status) AS TEXT) = '' OR payments.status = sqlc.arg(status))
  AND (CAST(sqlc.arg(provider) AS TEXT) = '' OR payments.provider = sqlc.arg(provider))
  AND (CAST(sqlc.arg(user_id) AS BIGINT) = 0 OR payments.user_id = sqlc.arg(user_id))
ORDER BY payments.id DESC LIMIT CAST(sqlc.arg(lim) AS BIGINT);

-- name: PaymentTotals :many
SELECT currency, count(*) AS n, CAST(COALESCE(sum(amount), 0) AS BIGINT) AS total
FROM payments WHERE status = 'applied' AND applied_at >= $1 GROUP BY currency;

-- name: ListTariffsOnSale :many
SELECT * FROM tariffs WHERE archived = 0 AND on_sale = 1 ORDER BY sort, id;
