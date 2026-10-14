package billing

import (
	"context"
	"database/sql"
	"errors"
	"strconv"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/events"
	"mikan/internal/panel/promo"
	"mikan/internal/panel/secure"
	"mikan/internal/panel/store/db"
)

// KindTrial is a payment for the trial when the admin puts a price on it.
const KindTrial = "trial"

// TrialRefundByHand is the error of a paid trial that may not be given and whose payment
// method cannot refund: the admin refunds it in the provider's dashboard and closes it with
// RefundedByHand. The reconcile pass leaves such a payment alone.
const TrialRefundByHand = "trial_refund_manual"

var (
	// ErrTrialOff: the admin offers no trial, or its tariff is gone.
	ErrTrialOff = errors.New("trial_off")
	// ErrTrialUsed: this Telegram account took its trial already, or is a customer (it
	// has a subscription or paid for one): a trial is for people who have not tried yet.
	ErrTrialUsed = errors.New("trial_used")
	// ErrTrialPaid: the trial has a price; it is bought with TrialInvoice.
	ErrTrialPaid = errors.New("trial_paid")
)

// errTrialChanged: a trial payment that was to be refunded is in a state nobody expected.
var errTrialChanged = errors.New("the trial payment changed while its money was going back")

// TrialTariff is the tariff a trial gives, when the admin offers one.
func (s *Service) TrialTariff(ctx context.Context) (db.Tariff, bool) {
	id := s.Config(ctx).TrialTariffID
	if id == 0 {
		return db.Tariff{}, false
	}
	t, err := s.d.Store.Q.GetTariff(ctx, id)
	// A tariff that lost its term since it was chosen would give a subscription with no end.
	if err != nil || t.Archived != 0 || t.DurationDays <= 0 {
		return db.Tariff{}, false
	}
	return t, true
}

// TrialOpen: tgID may take the free trial now. A hint for the bot's buttons; Trial
// decides on its own transaction.
func (s *Service) TrialOpen(ctx context.Context, tgID int64) bool {
	if _, ok := s.TrialTariff(ctx); !ok || s.Config(ctx).TrialPaid() {
		return false
	}
	ok, err := s.trialAllowed(ctx, s.d.Store.Q, tgID)
	return err == nil && ok
}

// TrialAllowed: tgID has not taken the trial and is not a customer yet, so it may take
// (or buy) the trial when one is offered. A hint for the bot's words.
func (s *Service) TrialAllowed(ctx context.Context, tgID int64) bool {
	ok, err := s.trialAllowed(ctx, s.d.Store.Q, tgID)
	return err == nil && ok
}

func (s *Service) trialAllowed(ctx context.Context, q *db.Queries, tgID int64) (bool, error) {
	if taken, err := q.HasTrial(ctx, tgID); err != nil || taken {
		return false, err
	}
	if n, err := q.CountTgLinksOf(ctx, tgID); err != nil || n > 0 {
		return false, err
	}
	n, err := q.CountUserPaidPayments(ctx, tgID)
	return n == 0, err
}

// trialAllowedFor: the account of payment pay may still be given the trial, asked while
// that payment is already paid, so it does not count against itself; nor does another
// trial payment paid meanwhile, or two paid at once would refuse each other: TakeTrial
// gives the trial to one of them. Everything else counts: a trial taken, a subscription of
// its own, a plan paid for. The trial a payment bought is given once, to someone who had
// nothing.
func (s *Service) trialAllowedFor(ctx context.Context, q *db.Queries, pay db.Payment) (bool, error) {
	if taken, err := q.HasTrial(ctx, pay.TgID); err != nil || taken {
		return false, err
	}
	if n, err := q.CountTgLinksOf(ctx, pay.TgID); err != nil || n > 0 {
		return false, err
	}
	n, err := q.CountUserPaidPaymentsExcept(ctx, db.CountUserPaidPaymentsExceptParams{TgID: pay.TgID, ID: pay.ID})
	return n == 0, err
}

// Trial gives tgID the free trial: a subscription on the trial tariff, linked to the
// account. Once per account: the trial is taken on the transaction that makes the
// subscription, so two taps (or two chats of one account) make one.
func (s *Service) Trial(ctx context.Context, tgID int64) (db.User, error) {
	t, ok := s.TrialTariff(ctx)
	if !ok {
		return db.User{}, ErrTrialOff
	}
	if s.Config(ctx).TrialPaid() {
		return db.User{}, ErrTrialPaid
	}
	var u db.User
	run := func(q *db.Queries) error {
		u = db.User{}
		allowed, err := s.trialAllowed(ctx, q, tgID)
		if err != nil {
			return err
		}
		if !allowed {
			return ErrTrialUsed
		}
		now := s.d.Now()
		if n, err := q.TakeTrial(ctx, db.TakeTrialParams{TgID: tgID, TariffID: sql.NullInt64{Int64: t.ID, Valid: true}, CreatedAt: now.Unix()}); err != nil {
			return err
		} else if n == 0 {
			return ErrTrialUsed
		}
		if u, err = s.d.Users.CreateOn(ctx, q, domain.CreateInput{Name: buyerName(ctx, q, tgID), Note: "Telegram · trial", TariffID: t.ID, Source: domain.UserFromTrial, TgID: tgID}, domain.Patch{}); err != nil {
			return err
		}
		if err := q.LinkTg(ctx, db.LinkTgParams{UserID: u.ID, TgID: tgID, CreatedAt: now.Unix()}); err != nil {
			return err
		}
		if err := q.SetTgCurrent(ctx, db.SetTgCurrentParams{Current: u.ID, TgID: tgID}); err != nil {
			return err
		}
		if err := q.SetTrialUser(ctx, db.SetTrialUserParams{UserID: sql.NullInt64{Int64: u.ID, Valid: true}, TgID: tgID}); err != nil {
			return err
		}
		// Told in the same transaction: never of a trial that rolled back.
		return emitTrialStarted(ctx, q, now, u.ID, tgID, t.ID, 0)
	}
	err := s.d.Store.Tx(ctx, run)
	if errors.Is(err, domain.ErrNoSlots) {
		if err = s.d.Users.RefillSlots(ctx); err == nil {
			err = s.d.Store.Tx(ctx, run)
		}
	}
	if err != nil {
		return db.User{}, err
	}
	s.d.Events.Kick()
	s.d.Users.Changed()
	s.d.Log.Info("billing: trial", "tg", tgID, "user", u.ID, "tariff", t.ID)
	return u, nil
}

// TrialOffer is the priced trial an account may buy: its tariff and the prices the
// available providers take (0: not for that currency).
type TrialOffer struct {
	Tariff db.Tariff
	Stars  int64
	Rub    int64 // kopecks
}

// TrialOffer is the priced trial tgID may buy now, with the providers available; ok false
// when the trial is free or off, the account may not take it, or no available provider
// takes its price. A hint for the bot and the Mini App; TrialInvoice and Apply decide on
// their own.
func (s *Service) TrialOffer(ctx context.Context, tgID int64) (TrialOffer, Available, bool) {
	c := s.Config(ctx)
	t, ok := s.TrialTariff(ctx)
	if !ok || !c.TrialPaid() {
		return TrialOffer{}, Available{}, false
	}
	av := s.Available(ctx)
	o := TrialOffer{Tariff: t}
	o.Stars, o.Rub = av.prices(trialPrice(c.TrialStars), trialPrice(c.TrialRub))
	if o.Stars == 0 && o.Rub == 0 {
		return o, av, false
	}
	allowed, err := s.trialAllowed(ctx, s.d.Store.Q, tgID)
	return o, av, err == nil && allowed
}

// trialPrice: a price of the trial as a tariff term's, unset when 0.
func trialPrice(n int64) sql.NullInt64 { return sql.NullInt64{Int64: n, Valid: n > 0} }

// TrialInvoice opens a payment for the priced trial: the trial tariff for its term at the
// trial's price, through any available provider. Only an account that may take the trial
// gets one (no trial, subscription or payment yet); the trial is taken when the payment
// applies. An open invoice for the same provider made in the last minutes is returned
// again, to an account that may still take the trial.
func (s *Service) TrialInvoice(ctx context.Context, tgID int64, provider string) (db.Payment, error) {
	c := s.Config(ctx)
	t, ok := s.TrialTariff(ctx)
	if !ok || !c.TrialPaid() {
		return db.Payment{}, ErrTrialOff
	}
	amount, currency, ok := s.Available(ctx).price(provider, trialPrice(c.TrialStars), trialPrice(c.TrialRub))
	if !ok {
		return db.Payment{}, ErrProviderOff
	}
	unlock, err := s.lockBuyer(ctx, tgID)
	if err != nil {
		return db.Payment{}, err
	}
	defer unlock()
	now := s.d.Now()
	tariffID := sql.NullInt64{Int64: t.ID, Valid: true}
	termDays := sql.NullInt64{Int64: t.DurationDays, Valid: true}
	// Asked on the invoice's transaction, under the account's lock, whether an open invoice
	// is given again or a new one made: a customer since the offer, or an account that
	// took its trial meanwhile, gets no pay link.
	eligible := func(q *db.Queries) error {
		allowed, err := s.trialAllowed(ctx, q, tgID)
		if err == nil && !allowed {
			err = ErrTrialUsed
		}
		return err
	}
	p, open, err := s.newPayment(ctx, tgID, now,
		func(q *db.Queries) ([]db.Payment, error) {
			return q.FindOpenPayments(ctx, db.FindOpenPaymentsParams{TgID: tgID, TariffID: tariffID, Provider: provider, Kind: KindTrial,
				Since: now.Add(-invoiceReuse).Unix()})
		},
		func(q *db.Queries, existing db.Payment) (bool, error) {
			if err := eligible(q); err != nil {
				return false, err
			}
			return existing.Amount == amount && existing.TermDays == termDays, nil
		},
		func(q *db.Queries) (db.Payment, error) {
			if err := eligible(q); err != nil {
				return db.Payment{}, err
			}
			return q.CreatePayment(ctx, db.CreatePaymentParams{Provider: provider, Payload: secure.Token(32), TgID: tgID, Kind: KindTrial,
				TariffID: tariffID, TariffName: t.Name, Amount: amount, Currency: currency, CreatedAt: now.Unix(), TermDays: termDays})
		})
	if err != nil || open {
		return p, err
	}
	lang, _ := s.d.Settings.Lang(ctx)
	return s.openPayment(ctx, p, t.Name, Describe(t, t.DurationDays, lang))
}

// applyTrial makes the paid trial on q's transaction: a subscription on the tariff and
// for the term the payment was made for, linked to the buyer, with the trial taken by the
// account. ErrTrialUsed when the account may no longer be given it (it took its trial,
// became a customer, or paid for a second trial): nothing is made then, and Apply gives
// the money back instead.
func (s *Service) applyTrial(ctx context.Context, q *db.Queries, pay db.Payment) (db.User, error) {
	allowed, err := s.trialAllowedFor(ctx, q, pay)
	if err != nil {
		return db.User{}, err
	}
	if !allowed {
		return db.User{}, ErrTrialUsed
	}
	now := s.d.Now()
	// The trial is taken on the transaction that gives it, so two invoices paid at once
	// make one trial: the second finds it taken and is given back.
	if taken, err := q.TakeTrial(ctx, db.TakeTrialParams{TgID: pay.TgID, TariffID: pay.TariffID, CreatedAt: now.Unix()}); err != nil {
		return db.User{}, err
	} else if taken == 0 {
		return db.User{}, ErrTrialUsed
	}
	// Bought like every other payment, with the trial's own source and note: a tariff
	// archived since the invoice still gives what was paid for.
	u, _, err := s.d.Users.Purchase(ctx, q, domain.PurchaseInput{TariffID: pay.TariffID.Int64, Term: pay.TermDays,
		Name: buyerName(ctx, q, pay.TgID), TgID: pay.TgID, Note: "Telegram · trial", Source: domain.UserFromTrial})
	if err != nil {
		return db.User{}, err
	}
	if err := q.LinkTg(ctx, db.LinkTgParams{UserID: u.ID, TgID: pay.TgID, CreatedAt: now.Unix()}); err != nil {
		return db.User{}, err
	}
	if err := q.SetTgCurrent(ctx, db.SetTgCurrentParams{Current: u.ID, TgID: pay.TgID}); err != nil {
		return db.User{}, err
	}
	return u, q.SetTrialUser(ctx, db.SetTrialUserParams{UserID: sql.NullInt64{Int64: u.ID, Valid: true}, TgID: pay.TgID})
}

// refundTrial gives back a paid trial that may no longer be given: the account took its
// trial, became a customer, or paid two trial invoices at once. Nothing was made for the
// payment, so it goes back whole and ends as refunded. A refund that fails (a bot that is
// down) leaves it paid with trial_refund_failed on it: the admin sees it on the Payments
// page and the reconcile pass keeps trying. An adapter without refunds is not asked at
// all: trialByHand leaves the payment to the admin.
func (s *Service) refundTrial(ctx context.Context, p db.Payment) error {
	// One refund of the payment at a time; it never gives up, as a refund already started
	// must finish.
	unlock, _ := s.trialRefunds.lock(context.Background(), p.ID)
	defer unlock()
	// Taken again under the lock: a webhook and the reconcile pass must not both refund.
	cur, err := s.d.Store.Q.GetPayment(ctx, p.ID)
	if err != nil {
		return err
	}
	if cur.Status != PaymentPaid {
		return nil
	}
	var refundErr error
	switch err := s.validatePromoProvider(ctx, cur.Provider); {
	case errors.Is(err, promo.ErrRefundUnsupported):
		return s.trialByHand(ctx, cur)
	case err != nil:
		// The adapter did not say what it can do (not answering now): not a refund by
		// hand, the next pass asks again.
		return err
	case !cur.ExternalID.Valid || cur.ExternalID.String == "":
		refundErr = errors.New("the payment carries no provider id to refund")
	default:
		refundErr = s.refundCharge(ctx, cur, cur.ExternalID.String, "mikan-trial-"+strconv.FormatInt(cur.ID, 10))
	}
	if refundErr != nil {
		s.d.Log.Error("billing: refund of a trial that cannot be given", "payment", cur.ID, "provider", cur.Provider, "err", refundErr)
		return errors.Join(refundErr, s.d.Store.Q.SetPaymentError(ctx, db.SetPaymentErrorParams{Error: "trial_refund_failed", ID: cur.ID}))
	}
	// Refunded before it was applied, as a late promo payment is: it never will be. The
	// addons hear of it in the same transaction, once (the dedupe key): when another way marked
	// it refunded meanwhile (Telegram's refunded_payment for the Stars just returned, the
	// admin's refund) the money is back all the same and none of those tells of a payment that
	// gave nothing. Anything else (another pass applied it) is the admin's to settle, said loudly.
	var refundedAt sql.NullInt64
	err = s.d.Store.Tx(ctx, func(q *db.Queries) error {
		now := s.d.Now()
		refundedAt = sql.NullInt64{Int64: now.Unix(), Valid: true}
		n, err := q.MarkLatePromoRefunded(ctx, db.MarkLatePromoRefundedParams{RefundedAt: refundedAt, ID: cur.ID})
		if err != nil {
			return err
		}
		if n != 1 {
			latest, err := q.GetPayment(ctx, cur.ID)
			if err != nil {
				return err
			}
			if latest.Status != PaymentRefunded {
				s.d.Log.Error("billing: the trial payment changed while its money was going back", "payment", cur.ID, "status", latest.Status)
				return errTrialChanged
			}
			refundedAt = latest.RefundedAt
		}
		if err := emitPayment(ctx, q, now, events.PaymentRefunded, cur, 0, false, false); err != nil {
			return err
		}
		return emitTrialNotGiven(ctx, q, now, cur, events.RefundedAuto)
	})
	if err != nil {
		return err
	}
	s.d.Events.Kick()
	s.d.Log.Warn("billing: a paid trial the account may not take was refunded", "payment", cur.ID, "provider", cur.Provider, "tg", cur.TgID)
	if n := s.notifier(); n != nil {
		// Nothing was made for the payment, so the buyer hears only that it came back.
		cur.Status, cur.RefundedAt = PaymentRefunded, refundedAt
		n.Refunded(ctx, cur, db.User{}, false)
	}
	return nil
}

// trialByHand leaves a paid trial the payment method cannot refund to the admin, once: the
// payment stays paid with TrialRefundByHand, which the reconcile pass does not retry, it is
// logged once, and the buyer is told the money comes back by hand. Nothing is made for it.
// Called again (a repeated report of the payment) it does nothing.
func (s *Service) trialByHand(ctx context.Context, p db.Payment) error {
	if p.Error == TrialRefundByHand {
		return nil
	}
	// The addons are told in the same transaction as the error is set, which says it was.
	err := s.d.Store.Tx(ctx, func(q *db.Queries) error {
		if err := q.SetPaymentError(ctx, db.SetPaymentErrorParams{Error: TrialRefundByHand, ID: p.ID}); err != nil {
			return err
		}
		return emitTrialNotGiven(ctx, q, s.d.Now(), p, events.RefundedManually)
	})
	if err != nil {
		return err
	}
	s.d.Events.Kick()
	s.d.Log.Warn("billing: a paid trial the account may not take waits for a refund by hand", "payment", p.ID, "provider", p.Provider, "tg", p.TgID)
	if n := s.notifier(); n != nil {
		n.TrialNotGiven(ctx, p)
	}
	return nil
}

// RefundedByHand closes a payment the admin refunded in the provider's dashboard: one paid
// through an adapter but never applied (a trial that could not be given, a subscription
// deleted since the invoice). It ends refunded, as a refund through the panel would, and
// the buyer is told the money is back. Stars are refunded through the panel (Refund); a
// payment applied, or in another state, is ErrNotRefunable.
func (s *Service) RefundedByHand(ctx context.Context, id int64) error {
	p, err := s.d.Store.Q.GetPayment(ctx, id)
	if err != nil {
		return err
	}
	if p.Provider == Stars || p.Status != PaymentPaid {
		return ErrNotRefunable
	}
	// Not while a trial's own refund runs (refundTrial takes the same turn).
	unlock, _ := s.trialRefunds.lock(context.Background(), id)
	defer unlock()
	refundedAt := sql.NullInt64{Int64: s.d.Now().Unix(), Valid: true}
	err = s.d.Store.Tx(ctx, func(q *db.Queries) error {
		n, err := q.MarkLatePromoRefunded(ctx, db.MarkLatePromoRefundedParams{RefundedAt: refundedAt, ID: id})
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrNotRefunable // applied or refunded meanwhile
		}
		return emitPayment(ctx, q, s.d.Now(), events.PaymentRefunded, p, 0, false, false)
	})
	if err != nil {
		return err
	}
	s.d.Events.Kick()
	if s.d.Promo != nil {
		if err := s.d.Promo.ReleasePayment(ctx, id); err != nil {
			s.d.Log.Warn("billing: release promo of a payment refunded by hand", "payment", id, "err", err)
		}
	}
	s.d.Log.Info("billing: refunded by hand", "payment", id, "provider", p.Provider)
	if n := s.notifier(); n != nil {
		p.Status, p.RefundedAt, p.Error = PaymentRefunded, refundedAt, ""
		n.Refunded(ctx, p, db.User{}, false)
	}
	return nil
}
