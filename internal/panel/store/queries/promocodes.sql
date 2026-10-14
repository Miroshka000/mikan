-- name: GetPromoCode :one
SELECT * FROM promo_codes WHERE id = sqlc.arg(id);

-- name: GetPromoCodeByCode :one
SELECT * FROM promo_codes WHERE code = sqlc.arg(code) AND deleted=0;

-- name: ListPromoCodes :many
SELECT * FROM promo_codes WHERE deleted = 0 ORDER BY id DESC LIMIT CAST(sqlc.arg(lim) AS BIGINT) OFFSET CAST(sqlc.arg(row_offset) AS BIGINT);

-- name: CountPromoCodes :one
SELECT COUNT(*) FROM promo_codes WHERE deleted = 0;

-- name: CountActivePromoCodes :one
SELECT COUNT(*) FROM promo_codes WHERE deleted=0 AND enabled=1
  AND (starts_at IS NULL OR starts_at<=sqlc.arg(now)) AND (ends_at IS NULL OR ends_at>sqlc.arg(now))
  AND (max_uses IS NULL OR used_count<max_uses);

-- name: CreatePromoCode :one
INSERT INTO promo_codes
(code,name,description,type,value,currency,starts_at,ends_at,max_uses,per_user_limit,discount_ttl,min_order,max_discount,tariff_ids,pool_id,first_purchase_only,new_users_only,enabled,deleted,created_at,created_by)
VALUES (sqlc.arg(code),sqlc.arg(name),sqlc.arg(description),sqlc.arg(type),sqlc.arg(value),sqlc.arg(currency),sqlc.narg(starts_at),sqlc.narg(ends_at),sqlc.narg(max_uses),sqlc.arg(per_user_limit),sqlc.arg(discount_ttl),sqlc.arg(min_order),sqlc.arg(max_discount),sqlc.arg(tariff_ids),sqlc.narg(pool_id),sqlc.arg(first_purchase_only),sqlc.arg(new_users_only),sqlc.arg(enabled),0,sqlc.arg(created_at),sqlc.narg(created_by)) RETURNING *;

-- name: UpdatePromoCode :one
UPDATE promo_codes SET
code=sqlc.arg(code),name=sqlc.arg(name),description=sqlc.arg(description),type=sqlc.arg(type),value=sqlc.arg(value),currency=sqlc.arg(currency),starts_at=sqlc.narg(starts_at),ends_at=sqlc.narg(ends_at),max_uses=sqlc.narg(max_uses),per_user_limit=sqlc.arg(per_user_limit),discount_ttl=sqlc.arg(discount_ttl),min_order=sqlc.arg(min_order),max_discount=sqlc.arg(max_discount),tariff_ids=sqlc.arg(tariff_ids),pool_id=sqlc.narg(pool_id),first_purchase_only=sqlc.arg(first_purchase_only),new_users_only=sqlc.arg(new_users_only),enabled=sqlc.arg(enabled) WHERE id=sqlc.arg(id) AND deleted=0 RETURNING *;

-- name: DeletePromoCode :execrows
UPDATE promo_codes SET deleted=1, enabled=0, pool_id=NULL WHERE id=sqlc.arg(id) AND deleted=0;

-- name: CountEnabledPromoCodesForPool :one
SELECT COUNT(*) FROM promo_codes WHERE pool_id=sqlc.arg(pool_id) AND enabled=1 AND deleted=0;

-- name: ClearDisabledPromoPools :exec
UPDATE promo_codes SET pool_id=NULL WHERE pool_id=sqlc.arg(pool_id) AND (enabled=0 OR deleted=1);

-- name: ClaimLatePromoRefund :execrows
UPDATE promo_redemptions SET refund_started_at=sqlc.arg(refund_started_at) WHERE id=sqlc.arg(id)
  AND (status='released' OR (status='reserved' AND expires_at IS NOT NULL AND expires_at<=sqlc.arg(now)))
  AND (refund_started_at IS NULL OR refund_started_at<=sqlc.arg(retry_after));

-- name: ReleaseLatePromoRefundClaim :exec
UPDATE promo_redemptions SET refund_started_at=NULL WHERE payment_id=sqlc.arg(payment_id);

-- name: ListLatePromoRefunds :many
SELECT id FROM payments WHERE error='promo_late_refund_failed' AND status IN ('pending','expired','paid','failed') ORDER BY id;

-- name: SetPromoEnabled :execrows
UPDATE promo_codes SET enabled=sqlc.arg(enabled) WHERE id=sqlc.arg(id) AND deleted=0;

-- name: IncrementPromoUse :execrows
UPDATE promo_codes SET used_count=used_count+1 WHERE id=sqlc.arg(id) AND (max_uses IS NULL OR used_count < max_uses) AND deleted=0 AND enabled=1;

-- name: RestorePromoUse :exec
UPDATE promo_codes SET used_count=used_count+1 WHERE id=sqlc.arg(id);

-- name: DecrementPromoUse :execrows
UPDATE promo_codes SET used_count=CASE WHEN used_count>0 THEN used_count-1 ELSE 0 END WHERE id=sqlc.arg(id);

-- name: CountUserPaidPayments :one
SELECT COUNT(*) FROM payments WHERE tg_id=sqlc.arg(tg_id) AND status IN ('paid','applied');

-- name: CountUserPaidPaymentsExcept :one
-- The same, apart from the trial's own payments: whether the account paid for anything
-- besides the trial being applied. Another trial payment paid meanwhile does not count:
-- TakeTrial gives the trial to one of them and the other goes back.
SELECT COUNT(*) FROM payments WHERE tg_id=sqlc.arg(tg_id) AND status IN ('paid','applied') AND id <> sqlc.arg(id) AND kind <> 'trial';

-- name: CountPromoUser :one
SELECT COUNT(*) FROM promo_redemptions WHERE promo_id=sqlc.arg(promo_id) AND (tg_id=sqlc.arg(tg_id) OR user_id=sqlc.arg(user_id)) AND status IN ('reserved','applied');

-- name: CreatePromoRedemption :one
INSERT INTO promo_redemptions
(promo_id,user_id,tg_id,payment_id,status,redeemed_at,expires_at,days,bytes,discount_amount,original_amount,final_amount,currency,note)
VALUES (sqlc.arg(promo_id),sqlc.narg(user_id),sqlc.arg(tg_id),sqlc.narg(payment_id),sqlc.arg(status),sqlc.arg(redeemed_at),sqlc.narg(expires_at),sqlc.arg(days),sqlc.arg(bytes),sqlc.arg(discount_amount),sqlc.arg(original_amount),sqlc.arg(final_amount),sqlc.arg(currency),sqlc.arg(note)) RETURNING *;

-- name: GetPromoRedemptionByPayment :one
SELECT * FROM promo_redemptions WHERE payment_id=sqlc.arg(payment_id);

-- name: ListPromoRedemptionsWithCode :many
SELECT r.*, c.code FROM promo_redemptions r JOIN promo_codes c ON c.id=r.promo_id
ORDER BY r.id DESC LIMIT CAST(sqlc.arg(lim) AS BIGINT) OFFSET CAST(sqlc.arg(row_offset) AS BIGINT);

-- name: CountPromoRedemptions :one
SELECT COUNT(*) FROM promo_redemptions;

-- name: MarkPromoApplied :execrows
UPDATE promo_redemptions SET status='applied', user_id=COALESCE(sqlc.narg(user_id),user_id) WHERE id=sqlc.arg(id) AND status IN ('reserved','released');

-- name: ReleasePromoRedemptionForClosedPayment :execrows
UPDATE promo_redemptions SET status='released'
WHERE promo_redemptions.id=sqlc.arg(id) AND promo_redemptions.status='reserved' AND EXISTS (
  SELECT 1 FROM payments p WHERE p.id=promo_redemptions.payment_id AND p.status IN ('failed','expired','refunded')
);

-- name: PromoStats :one
SELECT COUNT(CASE WHEN status='applied' THEN 1 END) AS successful_activations, CAST(COALESCE(SUM(CASE WHEN status='applied' THEN days ELSE 0 END),0) AS BIGINT) AS bonus_days, CAST(COALESCE(SUM(CASE WHEN status='applied' THEN bytes ELSE 0 END),0) AS BIGINT) AS bonus_bytes, CAST(COALESCE(SUM(CASE WHEN status='applied' THEN discount_amount ELSE 0 END),0) AS BIGINT) AS discount_amount, CAST(COALESCE(SUM(CASE WHEN status='applied' AND payment_id IS NOT NULL THEN 1 ELSE 0 END),0) AS BIGINT) AS discount_orders FROM promo_redemptions;

-- name: ListExpiredPromoPayments :many
SELECT DISTINCT p.id FROM payments p JOIN promo_redemptions r ON r.payment_id=p.id WHERE ((p.status='expired' AND p.created_at < sqlc.arg(before)) OR (r.expires_at IS NOT NULL AND r.expires_at<=sqlc.arg(before) AND p.status IN ('pending','expired','failed','refunded'))) AND r.status='reserved';

-- name: ReleaseExpiredPromoRedemption :execrows
UPDATE promo_redemptions SET status='released' WHERE promo_redemptions.id=sqlc.arg(id) AND promo_redemptions.status='reserved' AND (promo_redemptions.expires_at IS NOT NULL AND promo_redemptions.expires_at<=sqlc.arg(before) AND EXISTS (SELECT 1 FROM payments p WHERE p.id=promo_redemptions.payment_id AND p.status IN ('pending','expired','failed','refunded')) OR EXISTS (SELECT 1 FROM payments p WHERE p.id=promo_redemptions.payment_id AND p.status IN ('failed','expired','refunded') AND p.created_at<sqlc.arg(before)));

-- name: ListPromoRedemptionsByTgWithCode :many
SELECT r.*, c.code FROM promo_redemptions r JOIN promo_codes c ON c.id=r.promo_id
WHERE r.tg_id=sqlc.arg(tg_id) AND r.status='applied' ORDER BY r.id DESC LIMIT CAST(sqlc.arg(lim) AS BIGINT);

-- name: ReleaseAppliedPromoRedemption :execrows
UPDATE promo_redemptions SET status='released' WHERE id=sqlc.arg(id) AND status='applied';
