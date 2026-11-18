-- name: LockWebCheckout :exec
SELECT pg_advisory_xact_lock(hashtextextended('mikan-checkout:' || CAST(sqlc.arg(checkout_payload) AS TEXT), 0));

-- name: CreateWebCheckout :exec
INSERT INTO web_checkouts (payment_id, receipt_email) VALUES ($1, $2);

-- name: GetWebCheckout :one
SELECT * FROM web_checkouts WHERE payment_id = $1;

-- name: CompleteWebCheckout :exec
UPDATE web_checkouts SET receipt_email = '', token_hash = $2 WHERE payment_id = $1;

-- name: ClaimWebInvoice :execrows
INSERT INTO web_invoice_leases (payment_id, lease_until)
VALUES ($1, $2)
ON CONFLICT (payment_id) DO UPDATE SET lease_until = EXCLUDED.lease_until
WHERE web_invoice_leases.lease_until <= sqlc.arg(now);

-- name: ReleaseWebInvoice :exec
DELETE FROM web_invoice_leases WHERE payment_id = $1 AND lease_until = $2;

-- name: FinishWebInvoice :one
UPDATE payments AS p
SET external_id = $1, pay_url = $2, status = $3, error = $4
WHERE p.id = sqlc.arg(payment_id)
  AND p.status IN ('pending', 'failed') AND p.pay_url = '' AND p.external_id IS NULL
  AND EXISTS (SELECT 1 FROM web_invoice_leases l WHERE l.payment_id = p.id AND l.lease_until = sqlc.arg(lease_until))
RETURNING p.*;

-- name: ExpireIncompleteWebInvoice :exec
UPDATE payments AS p SET status = 'expired'
WHERE p.id = sqlc.arg(payment_id) AND p.status IN ('pending', 'failed')
  AND p.pay_url = '' AND p.external_id IS NULL
  AND NOT EXISTS (SELECT 1 FROM web_invoice_leases l WHERE l.payment_id = p.id AND l.lease_until > sqlc.arg(now));

-- name: ListExpiredWebInvoices :many
SELECT p.id, p.payload FROM payments AS p
WHERE p.created_at <= sqlc.arg(cutoff) AND p.status IN ('pending', 'failed')
  AND starts_with(p.payload, 'web_') AND p.pay_url = '' AND p.external_id IS NULL
  AND NOT EXISTS (SELECT 1 FROM web_invoice_leases l WHERE l.payment_id = p.id AND l.lease_until > sqlc.arg(now))
ORDER BY p.id LIMIT 100;
