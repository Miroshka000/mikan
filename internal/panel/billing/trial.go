package billing

import (
	"context"
	"database/sql"
	"errors"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/secure"
	"mikan/internal/panel/store/db"
)

// KindTrial is a payment for the trial when the admin puts a price on it.
const KindTrial = "trial"

var (
	// ErrTrialOff: the admin offers no trial, or its tariff is gone.
	ErrTrialOff = errors.New("trial_off")
	// ErrTrialUsed: this Telegram account took its trial already, or is a customer (it
	// has a subscription or paid for one): a trial is for people who have not tried yet.
	ErrTrialUsed = errors.New("trial_used")
	// ErrTrialPaid: the trial has a price; it is bought with TrialInvoice.
	ErrTrialPaid = errors.New("trial_paid")
)

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
		if u, err = s.d.Users.CreateOn(ctx, q, domain.CreateInput{Name: buyerName(ctx, q, tgID), Note: "Telegram · trial", TariffID: t.ID, Source: domain.UserFromTrial}, domain.Patch{}); err != nil {
			return err
		}
		if err := q.LinkTg(ctx, db.LinkTgParams{UserID: u.ID, TgID: tgID, CreatedAt: now.Unix()}); err != nil {
			return err
		}
		if err := q.SetTgCurrent(ctx, db.SetTgCurrentParams{Current: u.ID, TgID: tgID}); err != nil {
			return err
		}
		return q.SetTrialUser(ctx, db.SetTrialUserParams{UserID: sql.NullInt64{Int64: u.ID, Valid: true}, TgID: tgID})
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

// TrialOffer is the priced trial tgID may buy now; ok false when the trial is free or
// off, the account may not take it, or no available provider takes its price. A hint for
// the bot and the Mini App; TrialInvoice and Apply decide on their own.
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
// trial's price. Only an account that may take the trial gets one (no trial, subscription
// or payment yet); the trial is taken when the payment applies. An open invoice for the
// same provider made in the last minutes is returned again.
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
	p, open, err := s.newPayment(ctx, tgID, now,
		func(q *db.Queries) ([]db.Payment, error) {
			return q.FindOpenPayments(ctx, db.FindOpenPaymentsParams{TgID: tgID, TariffID: tariffID, Provider: provider, Kind: KindTrial,
				Since: now.Add(-invoiceReuse).Unix()})
		},
		func(_ *db.Queries, existing db.Payment) (bool, error) {
			return existing.Amount == amount && existing.TermDays == termDays, nil
		},
		func(q *db.Queries) (db.Payment, error) {
			// Checked under the account's lock: a customer since the offer gets no trial.
			if allowed, err := s.trialAllowed(ctx, q, tgID); err != nil {
				return db.Payment{}, err
			} else if !allowed {
				return db.Payment{}, ErrTrialUsed
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
// account. A second paid trial of one account (two invoices paid at once) still gives what
// was paid for; the trial stays taken by the first.
func (s *Service) applyTrial(ctx context.Context, q *db.Queries, pay db.Payment) (db.User, error) {
	now := s.d.Now()
	taken, err := q.TakeTrial(ctx, db.TakeTrialParams{TgID: pay.TgID, TariffID: pay.TariffID, CreatedAt: now.Unix()})
	if err != nil {
		return db.User{}, err
	}
	u, err := s.d.Users.CreateOn(ctx, q, domain.CreateInput{Name: buyerName(ctx, q, pay.TgID), Note: "Telegram · trial", TariffID: pay.TariffID.Int64,
		TermDays: pay.TermDays, Source: domain.UserFromTrial}, domain.Patch{})
	if err != nil {
		return db.User{}, err
	}
	if err := q.LinkTg(ctx, db.LinkTgParams{UserID: u.ID, TgID: pay.TgID, CreatedAt: now.Unix()}); err != nil {
		return db.User{}, err
	}
	if err := q.SetTgCurrent(ctx, db.SetTgCurrentParams{Current: u.ID, TgID: pay.TgID}); err != nil {
		return db.User{}, err
	}
	if taken > 0 {
		if err := q.SetTrialUser(ctx, db.SetTrialUserParams{UserID: sql.NullInt64{Int64: u.ID, Valid: true}, TgID: pay.TgID}); err != nil {
			return db.User{}, err
		}
	}
	return u, nil
}
