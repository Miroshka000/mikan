package billing

import (
	"context"
	"database/sql"
	"errors"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
)

// What the free plan asks of an account (Config.FreeCondition).
const (
	FreeNoCondition = ""
	FreeChannel     = "channel" // a subscription to the bot's channel
)

var (
	// ErrFreeOff: the admin offers no free plan, its tariff is gone, or it asks for the
	// bot's channel while none is set up.
	ErrFreeOff = errors.New("free_off")
	// ErrFreeActive: the account's previous free subscription has not ended yet.
	ErrFreeActive = errors.New("free_active")
	// ErrFreeChannel: the free plan is for the bot's channel's members, and the account is
	// not one.
	ErrFreeChannel = errors.New("free_channel")
)

// FreeTariff is the tariff the free plan gives, when the admin offers one.
func (s *Service) FreeTariff(ctx context.Context) (db.Tariff, bool) {
	id := s.Config(ctx).FreeTariffID
	if id == 0 {
		return db.Tariff{}, false
	}
	t, err := s.d.Store.Q.GetTariff(ctx, id)
	// As for the trial: a tariff without a term would give a free subscription that never
	// ends, and with it never another.
	if err != nil || t.Archived != 0 || t.DurationDays <= 0 {
		return db.Tariff{}, false
	}
	return t, true
}

// FreeOffer is the free plan as an account sees it: its tariff, and the link to the
// channel it asks the account to join ("" when it asks nothing).
type FreeOffer struct {
	Tariff  db.Tariff
	Channel string
}

// FreeOffer is the free plan tgID may take now; ok false when it is off, the account's
// previous free subscription has not ended, or the account holds as many subscriptions as
// it may. Whether the account is in the channel is not asked here: the offer shows the
// channel, FreePlan checks. A hint for the bot's buttons and the Mini App; FreePlan
// decides on its own transaction.
func (s *Service) FreeOffer(ctx context.Context, tgID int64) (FreeOffer, bool) {
	t, ok := s.FreeTariff(ctx)
	if !ok {
		return FreeOffer{}, false
	}
	o := FreeOffer{Tariff: t}
	if s.Config(ctx).FreeCondition == FreeChannel {
		if o.Channel, ok = s.channel(ctx); !ok {
			return FreeOffer{}, false
		}
	}
	q := s.d.Store.Q
	prev, err := q.GetFreePlanUser(ctx, tgID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return FreeOffer{}, false
	}
	if active, err := s.freeActive(ctx, q, prev); err != nil || active {
		return FreeOffer{}, false
	}
	n, err := q.CountTgLinksOf(ctx, tgID)
	return o, err == nil && n < s.d.MaxLinks
}

// channel is the link to the bot's channel; ok false without a bot or a channel.
func (s *Service) channel(ctx context.Context) (string, bool) {
	tg := s.telegram()
	if tg == nil {
		return "", false
	}
	return tg.Channel(ctx)
}

// freeActive: the free subscription prev (the account's last) still runs. A deleted one
// has ended, and so has one past its term; one the admin made endless never ends.
func (s *Service) freeActive(ctx context.Context, q *db.Queries, prev sql.NullInt64) (bool, error) {
	if !prev.Valid {
		return false, nil
	}
	u, err := q.GetUser(ctx, prev.Int64)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return !u.ExpiresAt.Valid || u.ExpiresAt.Int64 > s.d.Now().Unix(), nil
}

// FreePlan gives tgID the free plan: a subscription on its tariff, linked to the account.
// Again only once the previous free subscription has ended. The account's row is locked
// on the transaction that makes the subscription, so two taps, or the bot and the Mini
// App at once, make one.
func (s *Service) FreePlan(ctx context.Context, tgID int64) (db.User, error) {
	t, ok := s.FreeTariff(ctx)
	if !ok {
		return db.User{}, ErrFreeOff
	}
	if s.Config(ctx).FreeCondition == FreeChannel {
		if err := s.freeMember(ctx, tgID); err != nil {
			return db.User{}, err
		}
	}
	var u db.User
	run := func(q *db.Queries) error {
		u = db.User{}
		now := s.d.Now()
		prev, err := q.ClaimFreePlan(ctx, db.ClaimFreePlanParams{TgID: tgID, CreatedAt: now.Unix()})
		if err != nil {
			return err
		}
		if active, err := s.freeActive(ctx, q, prev); err != nil {
			return err
		} else if active {
			return ErrFreeActive
		}
		if n, err := q.CountTgLinksOf(ctx, tgID); err != nil {
			return err
		} else if n >= s.d.MaxLinks {
			return ErrTooManySubs
		}
		if u, err = s.d.Users.CreateOn(ctx, q, domain.CreateInput{Name: buyerName(ctx, q, tgID), Note: "Telegram · free plan", TariffID: t.ID, Source: domain.UserFromFree, TgID: tgID}, domain.Patch{}); err != nil {
			return err
		}
		if err := q.LinkTg(ctx, db.LinkTgParams{UserID: u.ID, TgID: tgID, CreatedAt: now.Unix()}); err != nil {
			return err
		}
		if err := q.SetTgCurrent(ctx, db.SetTgCurrentParams{Current: u.ID, TgID: tgID}); err != nil {
			return err
		}
		return q.SetFreePlanUser(ctx, db.SetFreePlanUserParams{UserID: sql.NullInt64{Int64: u.ID, Valid: true},
			TariffID: sql.NullInt64{Int64: t.ID, Valid: true}, CreatedAt: now.Unix(), TgID: tgID})
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
	s.d.Log.Info("billing: free plan", "tg", tgID, "user", u.ID, "tariff", t.ID)
	return u, nil
}

// freeMember checks the free plan's condition: tgID is in the bot's channel. A channel the
// bot cannot ask about (it is not the channel's admin, Telegram does not answer) lets the
// account through, as the bot's own gate does: the admin's Telegram page says what is
// wrong, and nobody is refused for the panel's trouble.
func (s *Service) freeMember(ctx context.Context, tgID int64) error {
	tg := s.telegram()
	if tg == nil {
		return ErrFreeOff
	}
	if _, ok := tg.Channel(ctx); !ok {
		return ErrFreeOff
	}
	member, err := tg.Subscribed(ctx, tgID)
	switch {
	case err != nil:
		s.d.Log.Warn("billing: the channel of the free plan cannot be checked, the account is let through", "tg", tgID, "err", err)
		return nil
	case !member:
		return ErrFreeChannel
	}
	return nil
}
