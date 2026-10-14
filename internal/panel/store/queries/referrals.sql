-- name: GetReferralAccount :one
SELECT * FROM referral_accounts WHERE tg_id = $1;

-- name: TakeReferralAccount :execrows
INSERT INTO referral_accounts (tg_id, code, created_at, updated_at) VALUES ($1, $2, $3, $3) ON CONFLICT (tg_id) DO NOTHING;

-- name: ClaimReferralCode :execrows
UPDATE referral_accounts SET code = sqlc.arg(code), updated_at = sqlc.arg(updated_at) WHERE tg_id = sqlc.arg(tg_id);

-- name: FindReferralAccountByCode :one
SELECT * FROM referral_accounts WHERE code = $1;

-- name: ReferralBalance :one
SELECT balance FROM referral_accounts WHERE tg_id = $1;

-- name: AddReferralBalance :execrows
UPDATE referral_accounts SET balance = balance + sqlc.arg(amount), updated_at = sqlc.arg(updated_at)
WHERE tg_id = sqlc.arg(tg_id) AND balance + sqlc.arg(amount) >= 0;

-- name: TakeReferralBalance :execrows
UPDATE referral_accounts SET balance = balance - sqlc.arg(amount), updated_at = sqlc.arg(updated_at)
WHERE tg_id = sqlc.arg(tg_id) AND balance >= sqlc.arg(amount);

-- name: LinkReferral :execrows
INSERT INTO referrals (referrer_tg_id, referee_tg_id, code, created_at)
VALUES ($1, $2, $3, $4) ON CONFLICT (referee_tg_id) DO NOTHING;

-- name: GetReferralOfReferee :one
SELECT * FROM referrals WHERE referee_tg_id = $1;

-- name: GetReferral :one
SELECT * FROM referrals WHERE id = $1;

-- name: MarkReferralPaid :execrows
UPDATE referrals SET first_paid_at = $2 WHERE id = $1 AND first_paid_at IS NULL;

-- name: ListReferralsOf :many
SELECT * FROM referrals WHERE referrer_tg_id = $1 ORDER BY id DESC LIMIT CAST(sqlc.arg(lim) AS BIGINT);

-- name: ListReferralsOfBefore :many
SELECT * FROM referrals WHERE referrer_tg_id = sqlc.arg(tg_id) AND id < sqlc.arg(before_id)
ORDER BY id DESC LIMIT CAST(sqlc.arg(lim) AS BIGINT);

-- name: CountReferralsOf :one
SELECT COUNT(*) AS invited,
  CAST(COALESCE(SUM(CASE WHEN first_paid_at IS NOT NULL THEN 1 ELSE 0 END), 0) AS BIGINT) AS paid
FROM referrals WHERE referrer_tg_id = $1;

-- name: ListRecentReferrals :many
SELECT r.*, COALESCE(a.code, '') AS referrer_code,
  CAST(COALESCE(ra.username, '') AS TEXT) AS referrer_username,
  CAST(COALESCE(rb.username, '') AS TEXT) AS referee_username
FROM referrals r
LEFT JOIN referral_accounts a ON a.tg_id = r.referrer_tg_id
LEFT JOIN tg_chats ra ON ra.tg_id = r.referrer_tg_id
LEFT JOIN tg_chats rb ON rb.tg_id = r.referee_tg_id
ORDER BY r.id DESC LIMIT CAST(sqlc.arg(lim) AS BIGINT) OFFSET CAST(sqlc.arg(row_offset) AS BIGINT);

-- name: CountReferrals :one
SELECT COUNT(*) AS total,
  CAST(COALESCE(SUM(CASE WHEN first_paid_at IS NOT NULL THEN 1 ELSE 0 END), 0) AS BIGINT) AS paid
FROM referrals;

-- name: CountReferralAccounts :one
SELECT COUNT(*) AS accounts,
  CAST(COALESCE(SUM(CASE WHEN balance > 0 THEN balance ELSE 0 END), 0) AS BIGINT) AS owed
FROM referral_accounts;

-- name: CreateReferralReward :one
INSERT INTO referral_rewards
(referrer_tg_id, referral_id, payment_id, rule_id, kind, amount, days, every_n, currency, created_at)
VALUES (sqlc.arg(referrer_tg_id), sqlc.arg(referral_id), sqlc.narg(payment_id), sqlc.arg(rule_id), sqlc.arg(kind), sqlc.arg(amount), sqlc.arg(days), sqlc.arg(every_n), sqlc.arg(currency), sqlc.arg(created_at))
RETURNING *;

-- name: GetReferralReward :one
SELECT * FROM referral_rewards WHERE id = $1;

-- name: ListReferralRewardsByPayment :many
SELECT * FROM referral_rewards WHERE payment_id = $1;

-- name: ListReferralRewardsOf :many
SELECT * FROM referral_rewards WHERE referrer_tg_id = $1 ORDER BY id DESC LIMIT CAST(sqlc.arg(lim) AS BIGINT);

-- name: ListReferralRewardsOfBefore :many
SELECT * FROM referral_rewards WHERE referrer_tg_id = sqlc.arg(tg_id) AND id < sqlc.arg(before_id)
ORDER BY id DESC LIMIT CAST(sqlc.arg(lim) AS BIGINT);

-- name: ListReferralRewards :many
SELECT r.*, CAST(COALESCE(c.username, '') AS TEXT) AS referrer_username, CAST(COALESCE(c.first_name, '') AS TEXT) AS referrer_name
FROM referral_rewards r LEFT JOIN tg_chats c ON c.tg_id = r.referrer_tg_id
ORDER BY r.id DESC LIMIT CAST(sqlc.arg(lim) AS BIGINT) OFFSET CAST(sqlc.arg(row_offset) AS BIGINT);

-- name: CountReferralRewards :one
SELECT COUNT(*) AS n,
  CAST(COALESCE(SUM(CASE WHEN kind = 'percent' AND reverted_at IS NULL THEN amount ELSE 0 END), 0) AS BIGINT) AS rub,
  CAST(COALESCE(SUM(CASE WHEN reverted_at IS NULL THEN days ELSE 0 END), 0) AS BIGINT) AS days
FROM referral_rewards;

-- name: RevertReferralReward :execrows
UPDATE referral_rewards SET reverted_at = $2 WHERE id = $1 AND reverted_at IS NULL;

-- name: RevertReferralRewardsByPayment :execrows
UPDATE referral_rewards SET reverted_at = sqlc.arg(reverted_at) WHERE payment_id = sqlc.arg(payment_id) AND reverted_at IS NULL;

-- name: AddReferralCount :one
INSERT INTO referral_counters (tg_id, rule_id, paid) VALUES ($1, $2, 1)
ON CONFLICT (tg_id, rule_id) DO UPDATE SET paid = referral_counters.paid + 1
RETURNING paid;

-- name: ListReferralCounts :many
SELECT * FROM referral_counters WHERE tg_id = $1;

-- name: CreateReferralSpend :one
INSERT INTO referral_spends (payment_id, tg_id, amount, created_at)
VALUES (sqlc.arg(payment_id), sqlc.arg(tg_id), sqlc.arg(amount), sqlc.arg(created_at)) RETURNING *;

-- name: GetReferralSpendByPayment :one
SELECT * FROM referral_spends WHERE payment_id = $1;

-- name: ListReferralSpendsOf :many
SELECT * FROM referral_spends WHERE tg_id = $1 ORDER BY id DESC LIMIT CAST(sqlc.arg(lim) AS BIGINT);

-- name: RevertReferralSpend :execrows
UPDATE referral_spends SET reverted_at = $2 WHERE payment_id = $1 AND reverted_at IS NULL;

-- name: ReleaseReferralSpend :execrows
UPDATE referral_spends SET reverted_at = $2 WHERE id = $1 AND reverted_at IS NULL;

-- name: ListSpentClosedPayments :many
-- Invoices whose balance hold must go back: nobody paid them.
SELECT s.id, s.payment_id FROM referral_spends s JOIN payments p ON p.id = s.payment_id
WHERE s.reverted_at IS NULL AND p.status IN ('expired','failed') ORDER BY s.id;

-- name: ReferralSpendTotals :one
SELECT COUNT(*) AS n, CAST(COALESCE(SUM(amount), 0) AS BIGINT) AS total
FROM referral_spends WHERE reverted_at IS NULL;