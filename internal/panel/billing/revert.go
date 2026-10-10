package billing

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
)

// A refund takes back what the payment gave. Apply records, on the transaction that
// applies a tariff payment, what it changed (payments.revert); a refund undoes exactly
// that. A traffic reset (renew_resets_traffic) cannot be undone: the used counter stays
// at zero, as it would after any renewal.

const day = int64(24 * time.Hour / time.Second)

// userState is the part of a user a purchase changes.
type userState struct {
	Status        string `json:"status"`
	TariffID      *int64 `json:"tariff_id"`
	TrafficLimit  *int64 `json:"traffic_limit"`
	DeviceLimit   *int64 `json:"device_limit"`
	SpeedLimit    *int64 `json:"speed_limit,omitempty"` // payments before speed caps have none: no cap then
	ResetStrategy string `json:"reset_strategy"`
	BillingDay    *int64 `json:"billing_day"`
	ExpiresAt     *int64 `json:"expires_at"`
}

// revertInfo is payments.revert: the user the payment created, or the user before (Prior)
// and right after (Set) a renewal.
type revertInfo struct {
	Created bool       `json:"created,omitempty"`
	Prior   *userState `json:"prior,omitempty"`
	Set     *userState `json:"set,omitempty"`
}

func nullPtr(n sql.NullInt64) *int64 {
	if !n.Valid {
		return nil
	}
	return &n.Int64
}

func ptrNull(p *int64) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *p, Valid: true}
}

func stateOf(u db.User) *userState {
	return &userState{Status: u.Status, TariffID: nullPtr(u.TariffID), TrafficLimit: nullPtr(u.TrafficLimit), DeviceLimit: nullPtr(u.DeviceLimit),
		SpeedLimit: nullPtr(u.SpeedLimit), ResetStrategy: u.ResetStrategy, BillingDay: nullPtr(u.BillingDay), ExpiresAt: nullPtr(u.ExpiresAt)}
}

// sameOffer: the user still has the tariff and limits the payment set. The term and the
// status are the admin's to change, so they are not compared.
func (s *userState) sameOffer(u db.User) bool {
	o := stateOf(u)
	return eqPtr(s.TariffID, o.TariffID) && eqPtr(s.TrafficLimit, o.TrafficLimit) && eqPtr(s.DeviceLimit, o.DeviceLimit) && eqPtr(s.SpeedLimit, o.SpeedLimit) && eqPtr(s.BillingDay, o.BillingDay) &&
		s.ResetStrategy == o.ResetStrategy
}

func eqPtr(a, b *int64) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}

// purchaseRevert is what Apply records for a tariff payment: prior is the user before the
// purchase (hadPrior false: there was none, the payment created u).
func purchaseRevert(created, hadPrior bool, prior, u db.User) string {
	// Set is kept for a created user too: a later renewal may build on that term.
	ri := revertInfo{Created: created, Set: stateOf(u)}
	if !created && hadPrior {
		ri.Prior = stateOf(prior)
	}
	b, _ := json.Marshal(ri)
	return string(b)
}

// Reverted says what a refund took back, for the audit log and the buyer's message.
type Reverted struct {
	// Action: "grant" (the package's traffic removed), "disabled" (the subscription the
	// payment created turned off), "term" (the term the payment added taken back), "none".
	Action         string `json:"action"`
	TariffRestored bool   `json:"tariff_restored,omitempty"`
	PromoReleased  bool   `json:"promo_released,omitempty"`
	// Exact: taken back from what Apply recorded; false for a payment applied before that.
	Exact bool `json:"exact,omitempty"`
}

// takeBack undoes what the applied payment p gave, on q's transaction. u is the user
// after it (zero when the payment's subscription is gone).
func (s *Service) takeBack(ctx context.Context, q *db.Queries, p db.Payment) (rv Reverted, u db.User, err error) {
	rv.Action = "none"
	if s.d.Promo != nil {
		if rv.PromoReleased, err = s.d.Promo.ReleaseApplied(ctx, q, p.ID); err != nil {
			return rv, u, err
		}
	}
	if !p.UserID.Valid {
		return rv, u, nil
	}
	if u, err = q.GetUser(ctx, p.UserID.Int64); errors.Is(err, sql.ErrNoRows) {
		return rv, db.User{}, nil
	} else if err != nil {
		return rv, u, err
	}
	if p.Kind == KindPackage {
		// The grant goes whole: what was used of it is already counted as used.
		n, err := q.DeleteGrantByPayment(ctx, sql.NullInt64{Int64: p.ID, Valid: true})
		if n > 0 {
			rv.Action = "grant"
		}
		return rv, u, err
	}
	return s.takeBackTariff(ctx, q, p, u, rv)
}

func (s *Service) takeBackTariff(ctx context.Context, q *db.Queries, p db.Payment, u db.User, rv Reverted) (Reverted, db.User, error) {
	var ri revertInfo
	rv.Exact = p.Revert != "" && json.Unmarshal([]byte(p.Revert), &ri) == nil
	if !rv.Exact {
		// Applied before the snapshot existed: a new subscription is turned off, a renewal
		// loses the days of its term.
		ri = revertInfo{Created: p.Kind == "new"}
	}
	now := s.d.Now().Unix()
	// Payments after this one build on it: the subscription stays, only this term goes.
	later, err := q.CountLaterTariffPayments(ctx, db.CountLaterTariffPaymentsParams{UserID: p.UserID, ID: p.ID, AppliedAt: p.AppliedAt.Int64})
	if err != nil {
		return rv, u, err
	}
	par := userParams(u, now)
	if ri.Created && later == 0 {
		// Disabled, not deleted: the buyer's Telegram link and the subscription stay.
		par.Status, par.ExpiresAt = "disabled", sql.NullInt64{Int64: now, Valid: true}
		rv.Action = "disabled"
		u, err = q.UpdateUser(ctx, par)
		return rv, u, err
	}
	days, err := s.termOf(ctx, q, p)
	if err != nil {
		return rv, u, err
	}
	par.ExpiresAt = rolledBack(u.ExpiresAt, ri, p.AppliedAt.Int64, days, now)
	rv.Action = "term"
	var restoreTariff int64
	if ri.Prior != nil && ri.Set != nil && later == 0 && ri.Set.sameOffer(u) && u.Status == ri.Set.Status {
		// Nothing changed the offer since: the prior tariff, limits and status come back.
		ok, err := s.tariffExists(ctx, q, ri.Prior.TariffID)
		if err != nil {
			return rv, u, err
		}
		if ok {
			par.Status, par.TariffID, par.TrafficLimit, par.DeviceLimit = ri.Prior.Status, ptrNull(ri.Prior.TariffID), ptrNull(ri.Prior.TrafficLimit), ptrNull(ri.Prior.DeviceLimit)
			par.ResetStrategy, par.BillingDay, par.SpeedLimit = ri.Prior.ResetStrategy, ptrNull(ri.Prior.BillingDay), ptrNull(ri.Prior.SpeedLimit)
			rv.TariffRestored = true
			if ri.Prior.TariffID != nil {
				restoreTariff = *ri.Prior.TariffID
			}
		}
	}
	if u, err = q.UpdateUser(ctx, par); err != nil {
		return rv, u, err
	}
	switch {
	case !rv.TariffRestored:
	case restoreTariff != 0:
		err = domain.ApplyTariffPools(ctx, q, u.ID, restoreTariff)
	default:
		err = q.ClearUserPoolLimits(ctx, u.ID)
	}
	return rv, u, err
}

func (s *Service) tariffExists(ctx context.Context, q *db.Queries, id *int64) (bool, error) {
	if id == nil {
		return true, nil
	}
	if _, err := q.GetTariff(ctx, *id); errors.Is(err, sql.ErrNoRows) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return true, nil
}

// termOf is the days of the term the payment bought, as bought.
func (s *Service) termOf(ctx context.Context, q *db.Queries, p db.Payment) (int64, error) {
	if p.TermDays.Valid {
		return p.TermDays.Int64, nil
	}
	if !p.TariffID.Valid {
		return 0, nil
	}
	t, err := q.GetTariff(ctx, p.TariffID.Int64)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return t.DurationDays, err
}

// userParams is u as it is, to change some of it.
func userParams(u db.User, now int64) db.UpdateUserParams {
	return db.UpdateUserParams{Name: u.Name, Contact: u.Contact, Note: u.Note, Tags: u.Tags, Status: u.Status, TariffID: u.TariffID,
		TrafficLimit: u.TrafficLimit, DeviceLimit: u.DeviceLimit, SpeedLimit: u.SpeedLimit, ResetStrategy: u.ResetStrategy, PeriodDays: u.PeriodDays, PeriodStart: u.PeriodStart,
		ExpiresAt: u.ExpiresAt, Inbounds: u.Inbounds, BillingDay: u.BillingDay, UpdatedAt: now, ID: u.ID}
}

// rolledBack is the end of the user's access (cur) with the term the payment added taken
// back: what it added is its end minus the later of the old end and its time (just its
// time when there was no old end, or it was unlimited). Never below now: access that would
// end earlier ends now. Unlimited (null) ends are left alone when the term cannot be worked
// out. days is the payment's term, used when nothing was recorded.
//
// When a payment or an edit came after this one, the added span is subtracted in seconds.
// For a term counted in months (billing_day) that is an approximation: the later payment
// may have counted its months from a different day.
func rolledBack(cur sql.NullInt64, ri revertInfo, appliedAt, days, now int64) sql.NullInt64 {
	var next sql.NullInt64
	switch {
	case ri.Set != nil:
		set := ptrNull(ri.Set.ExpiresAt)
		base := appliedAt
		if ri.Prior != nil && ri.Prior.ExpiresAt != nil {
			base = max(*ri.Prior.ExpiresAt, appliedAt)
		}
		switch {
		case cur == set && ri.Prior != nil && ri.Prior.ExpiresAt == nil:
			// Unlimited before: unlimited again.
		case cur == set && ri.Prior != nil && !set.Valid:
			// The payment made it unlimited: the old end comes back as it was.
			next = ptrNull(ri.Prior.ExpiresAt)
		case cur == set && ri.Prior != nil:
			next = sql.NullInt64{Int64: base, Valid: true}
		case !cur.Valid || !set.Valid:
			return cur
		default:
			next = sql.NullInt64{Int64: cur.Int64 - (set.Int64 - base), Valid: true}
		}
	case cur.Valid && days > 0:
		next = sql.NullInt64{Int64: cur.Int64 - days*day, Valid: true}
	default:
		return cur
	}
	if next.Valid && next.Int64 < now {
		// Already over stays as it is; otherwise it ends now.
		if cur.Valid && cur.Int64 < now {
			return cur
		}
		next.Int64 = now
	}
	return next
}

// refunded records a refunded payment: it is marked refunded and what it gave is taken
// back on one transaction, so a second call for the same refund (the admin's button and
// Telegram's update about it) finds it done and changes nothing.
func (s *Service) refunded(ctx context.Context, id int64) (Reverted, error) {
	var (
		p          db.Payment
		u          db.User
		rv         Reverted
		done, open bool
	)
	err := s.d.Store.Tx(ctx, func(q *db.Queries) error {
		// A conflict runs this again: nothing from an attempt that rolled back may stay.
		p, u, rv, done, open = db.Payment{}, db.User{}, Reverted{Action: "none"}, false, false
		var err error
		if p, err = q.GetPayment(ctx, id); err != nil {
			return err
		}
		at := sql.NullInt64{Int64: s.d.Now().Unix(), Valid: true}
		switch p.Status {
		case "applied":
			n, err := q.MarkPaymentRefunded(ctx, db.MarkPaymentRefundedParams{RefundedAt: at, ID: id})
			if err != nil || n != 1 {
				return err
			}
			done = true
			rv, u, err = s.takeBack(ctx, q, p)
			return err
		case "pending", "expired", "paid", "failed":
			// Refunded before it was applied: it never will be.
			n, err := q.MarkLatePromoRefunded(ctx, db.MarkLatePromoRefundedParams{RefundedAt: at, ID: id})
			open = n == 1
			return err
		}
		return nil
	})
	if err != nil {
		return Reverted{}, err
	}
	if open && s.d.Promo != nil {
		if err := s.d.Promo.ReleasePayment(ctx, id); err != nil {
			s.d.Log.Warn("billing: release promo of a refunded payment", "payment", id, "err", err)
		}
	}
	if !done {
		return rv, nil
	}
	s.d.Users.Changed()
	s.d.Log.Info("billing: refunded", "payment", id, "action", rv.Action, "exact", rv.Exact)
	if tg := s.telegram(); tg != nil {
		p.Status = "refunded"
		tg.Refunded(ctx, p, u, rv.Action == "disabled")
	}
	return rv, nil
}
