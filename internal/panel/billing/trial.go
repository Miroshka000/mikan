package billing

import (
	"context"
	"database/sql"
	"errors"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
)

var (
	// ErrTrialOff: the admin offers no trial, or its tariff is gone.
	ErrTrialOff = errors.New("trial_off")
	// ErrTrialUsed: this Telegram account took its trial already, or is a customer (it
	// has a subscription or paid for one): a trial is for people who have not tried yet.
	ErrTrialUsed = errors.New("trial_used")
)

// TrialTariff is the tariff a trial gives, when the admin offers one.
func (s *Service) TrialTariff(ctx context.Context) (db.Tariff, bool) {
	id := s.Config(ctx).TrialTariffID
	if id == 0 {
		return db.Tariff{}, false
	}
	t, err := s.d.Store.Q.GetTariff(ctx, id)
	if err != nil || t.Archived != 0 {
		return db.Tariff{}, false
	}
	return t, true
}

// TrialOpen: tgID may take the free trial now. A hint for the bot's buttons; Trial
// decides on its own transaction.
func (s *Service) TrialOpen(ctx context.Context, tgID int64) bool {
	if _, ok := s.TrialTariff(ctx); !ok {
		return false
	}
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
		if u, err = s.d.Users.CreateOn(ctx, q, domain.CreateInput{Name: buyerName(ctx, q, tgID), Note: "Telegram · trial", TariffID: t.ID}, domain.Patch{}); err != nil {
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
