package billing

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"mikan/internal/panel/promo"
	"mikan/internal/panel/store/db"
)

// A payment that comes after its promo discount ran out (promo.PaidLate) buys nothing at
// that price: its money goes back to the buyer, through Telegram for Stars or the
// adapter's refund for the rest, and the reservation is released. Webhooks, the bot's
// update stream and the reconcile pass may all meet the same late payment: the steps
// below take a lock per payment and a claim in the database, so it is refunded once.

func paymentTime(p db.Payment, fallback int64) int64 {
	if p.PaidAt.Valid {
		return p.PaidAt.Int64
	}
	return fallback
}

func (s *Service) refundPromoPayment(ctx context.Context, p db.Payment, externalID string) error {
	return s.refundCharge(ctx, p, externalID, "mikan-promo-late-"+strconv.FormatInt(p.ID, 10))
}

// refundCharge gives a payment's money back through the provider it came by: Stars
// through the bot, an adapter's payment through the adapter, with key as the adapter's
// idempotency key (one per payment and reason), so a retry after a restart does not
// refund twice. The adapter is asked for the invoice's amount: a provider's fee the buyer
// paid on top (Platega adds its own) is not known to the panel and stays with the
// provider, for a late promo payment and a trial alike.
func (s *Service) refundCharge(ctx context.Context, p db.Payment, externalID, key string) error {
	switch {
	case p.Provider == Stars:
		tg := s.telegram()
		if tg == nil {
			return errors.New("cannot refund a Stars payment: Telegram bot is unavailable")
		}
		return tg.RefundStars(ctx, p.TgID, externalID)
	case AddonID(p.Provider) != "":
		cl, cfg, err := s.addonClient(ctx, AddonID(p.Provider))
		if err != nil {
			return err
		}
		return cl.Refund(ctx, cfg.Values, externalID, p.Amount, key)
	default:
		return errors.New("cannot refund a payment from this provider")
	}
}

// refundLatePromoPayment serializes late-payment refunds within the panel process and
// reloads persistent state after taking the lock so webhook and reconciliation cannot
// both refund the same invoice. Addon refunds also carry a stable provider idempotency key
// to cover retries after a process restart.
func (s *Service) refundLatePromoPayment(ctx context.Context, paymentID int64, externalID string, refund func(db.Payment) error) (bool, error) {
	unlock := s.lockPromoRefund(paymentID)
	defer unlock()

	pay, err := s.d.Store.Q.GetPayment(ctx, paymentID)
	if err != nil {
		return false, err
	}
	if pay.Status == PaymentRefunded {
		if err := s.d.Promo.ReleasePayment(ctx, paymentID); err != nil {
			return true, err
		}
		return true, nil
	}
	r, err := s.d.Promo.GetPaymentRedemption(ctx, paymentID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	late := promo.PaidLate(r, paymentTime(pay, s.d.Now().Unix()))
	if !late {
		return false, nil
	}
	if externalID != "" {
		if err := s.d.Store.Q.SetPaymentExternalID(ctx, db.SetPaymentExternalIDParams{ExternalID: sql.NullString{String: externalID, Valid: true}, ID: paymentID}); err != nil {
			return true, s.markLatePromoRefundFailed(ctx, paymentID, err)
		}
	}
	n, err := s.d.Store.Q.ClaimLatePromoRefund(ctx, db.ClaimLatePromoRefundParams{
		RefundStartedAt: sql.NullInt64{Int64: s.d.Now().Unix(), Valid: true},
		ID:              r.ID,
		Now:             sql.NullInt64{Int64: s.d.Now().Unix(), Valid: true},
		RetryAfter:      sql.NullInt64{Int64: s.d.Now().Add(-2 * time.Minute).Unix(), Valid: true},
	})
	if err != nil {
		return true, err
	}
	if n != 1 {
		// Another panel process owns the current refund attempt. Its stable provider key
		// makes retry after a crashed process safe once the lease expires.
		return true, nil
	}
	if err := refund(pay); err != nil {
		_ = s.markLatePromoRefundFailed(ctx, paymentID, err)
		_ = s.d.Store.Q.ReleaseLatePromoRefundClaim(ctx, sql.NullInt64{Int64: paymentID, Valid: true})
		return true, err
	}
	n, err = s.d.Store.Q.MarkLatePromoRefunded(ctx, db.MarkLatePromoRefundedParams{
		RefundedAt: sql.NullInt64{Int64: s.d.Now().Unix(), Valid: true}, ID: paymentID,
	})
	if err != nil {
		return true, s.markLatePromoRefundFailed(ctx, paymentID, err)
	}
	if n != 1 {
		latest, err := s.d.Store.Q.GetPayment(ctx, paymentID)
		if err != nil {
			return true, err
		}
		if latest.Status != PaymentRefunded {
			return true, s.markLatePromoRefundFailed(ctx, paymentID, errors.New("promo late refund completed but payment status changed"))
		}
	}
	if err := s.d.Promo.ReleasePayment(ctx, paymentID); err != nil {
		return true, s.markLatePromoRefundFailed(ctx, paymentID, err)
	}
	s.d.Log.Warn("billing: late discounted payment refunded", "payment", paymentID, "provider", pay.Provider)
	return true, nil
}

func (s *Service) markLatePromoRefundFailed(ctx context.Context, paymentID int64, cause error) error {
	if err := s.d.Store.Q.SetPaymentError(ctx, db.SetPaymentErrorParams{Error: "promo_late_refund_failed", ID: paymentID}); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

// lockPromoRefund waits for the payment's turn at a late-promo refund; it never gives up,
// as a refund already started must finish.
func (s *Service) lockPromoRefund(paymentID int64) func() {
	unlock, _ := s.promoRefunds.lock(context.Background(), paymentID)
	return unlock
}
