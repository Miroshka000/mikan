package domain

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"mikan/internal/panel/audit"
	"mikan/internal/panel/events"
	"mikan/internal/panel/events/feed"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

// The next tariff: the admin sets the tariff a subscription moves to once its term ends,
// and the housekeeping switches it then, once (SwitchDue). The switch is the tariff's as a
// renewal onto it would be, without a payment: its limits, a term of its own from the old
// term's end, a new traffic period (with what the old one left, by the carry-over rules).
// The subscription stays on or off as it was: the admin who switched it off decides.

// SwitchAhead is how long before a term ends its next tariff takes over: one round of the
// housekeeping (app.maintain, every ten minutes), so that no round in between finds the
// subscription over and cuts it off. The new term starts at the old one's end all the same.
const SwitchAhead = 10 * time.Minute

// NextTariff is the tariff a subscription moves to at the end of its term: TermDays on it
// (invalid: the tariff's first term).
type NextTariff struct {
	TariffID int64
	TermDays sql.NullInt64
}

// NextTariffOf is userID's next tariff; ok false: none.
func NextTariffOf(ctx context.Context, q *db.Queries, userID int64) (NextTariff, bool, error) {
	n, err := q.GetNextTariff(ctx, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return NextTariff{}, false, nil
	}
	return NextTariff{TariffID: n.TariffID, TermDays: n.TermDays}, err == nil, err
}

// NextTariffsOf are the next tariffs of the users of ids that have one: a page's worth in
// one read.
func NextTariffsOf(ctx context.Context, q *db.Queries, ids []int64) (map[int64]NextTariff, error) {
	rows, err := q.ListNextTariffsOf(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]NextTariff, len(rows))
	for _, r := range rows {
		out[r.UserID] = NextTariff{TariffID: r.TariffID, TermDays: r.TermDays}
	}
	return out, nil
}

// SetNextTariff sets the tariff userID moves to when its term ends. The tariff is one on
// offer (not archived), the term 0–MaxExtendDays days (0: no end). A subscription without an
// end has nothing to follow. One whose term is over, or ends before the housekeeping's next
// round, switches now: switched says so.
func (s *Users) SetNextTariff(ctx context.Context, userID int64, n NextTariff) (u db.User, switched bool, err error) {
	if n.TermDays.Valid && (n.TermDays.Int64 < 0 || n.TermDays.Int64 > MaxExtendDays) {
		return db.User{}, false, fieldErr("term_days", "bad_days")
	}
	err = s.st.Tx(ctx, func(q *db.Queries) error {
		switched = false
		if _, err := q.LockUser(ctx, userID); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if u, err = q.GetUser(ctx, userID); err != nil {
			return err
		}
		if !u.ExpiresAt.Valid {
			return fieldErr("tariff_id", "no_term_end")
		}
		t, err := q.GetTariff(ctx, n.TariffID)
		if errors.Is(err, sql.ErrNoRows) || err == nil && t.Archived != 0 {
			return fieldErr("tariff_id", "tariff_not_found")
		}
		if err != nil {
			return err
		}
		now := s.now()
		if err := q.SetNextTariff(ctx, db.SetNextTariffParams{UserID: userID, TariffID: t.ID, TermDays: n.TermDays, CreatedAt: now.Unix()}); err != nil {
			return err
		}
		waiting, err := Waiting(ctx, q, userID)
		if err != nil || waiting || store.Time(u.ExpiresAt.Int64).After(now.Add(SwitchAhead)) {
			return err
		}
		u, err = s.switchNext(ctx, q, u, now)
		switched = err == nil
		return err
	})
	if err != nil {
		return db.User{}, false, err
	}
	if switched {
		s.hub.Kick()
		s.changes.PoliciesChanged()
	}
	return u, switched, nil
}

// ClearNextTariff takes userID's next tariff away; the term ends as it would.
func (s *Users) ClearNextTariff(ctx context.Context, userID int64) error {
	if _, err := s.Get(ctx, userID); err != nil {
		return err
	}
	_, err := s.st.Q.DeleteNextTariff(ctx, userID)
	return err
}

// SwitchDue switches every subscription whose term ends within SwitchAhead to its next
// tariff: the housekeeping's round. Each in a transaction of its own that reads the
// subscription again under its lock (a payment may have renewed it meanwhile, and its term
// then ends later); one that fails does not hold the others back. It returns how many
// switched, and what failed.
func (s *Users) SwitchDue(ctx context.Context) (int, error) {
	now := s.now()
	ids, err := s.st.Q.ListDueNextTariffs(ctx, now.Add(SwitchAhead).Unix())
	if err != nil {
		return 0, err
	}
	n := 0
	var errs []error
	for _, id := range ids {
		switched := false
		err := s.st.Tx(ctx, func(q *db.Queries) error {
			switched = false
			if _, err := q.LockUser(ctx, id); errors.Is(err, sql.ErrNoRows) {
				return nil
			} else if err != nil {
				return err
			}
			u, err := q.GetUser(ctx, id)
			if err != nil {
				return err
			}
			waiting, err := Waiting(ctx, q, id)
			if err != nil || waiting || !u.ExpiresAt.Valid || store.Time(u.ExpiresAt.Int64).After(now.Add(SwitchAhead)) {
				return err
			}
			if _, err := s.switchNext(ctx, q, u, now); errors.Is(err, sql.ErrNoRows) {
				// The next tariff went meanwhile (the admin took it away, its tariff deleted).
				return nil
			} else if err != nil {
				return err
			}
			switched = true
			return nil
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("switch user %d: %w", id, err))
			continue
		}
		if switched {
			n++
		}
	}
	if n > 0 {
		s.hub.Kick()
		s.changes.PoliciesChanged()
	}
	return n, errors.Join(errs...)
}

// switchNext moves u, locked on q's transaction, to its next tariff as of now: sql.ErrNoRows
// when it has none. The new term starts at the old one's end, or now when that is past (the
// panel was down when it came); the tariff's limits, device mode and server groups apply, a
// new traffic period begins with what the old one left (carryInto), the next tariff is used
// up, and the journal and the addons hear of it.
func (s *Users) switchNext(ctx context.Context, q *db.Queries, u db.User, now time.Time) (db.User, error) {
	n, err := q.GetNextTariff(ctx, u.ID)
	if err != nil {
		return u, err
	}
	t, err := q.GetTariff(ctx, n.TariffID)
	if err != nil {
		return u, err
	}
	carried, err := carryInto(ctx, q, u, t)
	if err != nil {
		return u, err
	}
	base := now
	if u.ExpiresAt.Valid && store.Time(u.ExpiresAt.Int64).After(now) {
		base = store.Time(u.ExpiresAt.Int64)
	}
	from := u.TariffID
	// Nothing is paid for the switch: places bought on top stay only while they were bought
	// for, as with a renewal onto another tariff, and the subscription stays on or off.
	if u, err = takeTariff(ctx, q, u, t, tariffTake{base: base, days: termDays(t, n.TermDays)}, now); err != nil {
		return u, err
	}
	if err := StartPeriod(ctx, q, u.ID, now.Unix(), now); err != nil {
		return u, err
	}
	if u, err = q.GetUser(ctx, u.ID); err != nil {
		return u, err
	}
	if err := grantCarry(ctx, q, u, carried, 0, now); err != nil {
		return u, err
	}
	if _, err := q.DeleteNextTariff(ctx, u.ID); err != nil {
		return u, err
	}
	id := strconv.FormatInt(u.ID, 10)
	if err := audit.Write(ctx, q, now, audit.Entry{Action: "user.switch_tariff", TargetType: "user", TargetID: id,
		Details: map[string]any{"tariff_id": t.ID, "from_tariff_id": from.Int64, "term_days": termDays(t, n.TermDays), "carried": carried}}); err != nil {
		return u, err
	}
	tg, err := linkedAccount(ctx, q, u.ID)
	if err != nil {
		return u, err
	}
	return u, feed.Emit(ctx, q, now, events.UserSwitched, u.ID, tg,
		events.Switched{TariffID: t.ID, FromTariffID: from.Int64, ExpiresAt: u.ExpiresAt.Int64, Carried: carried}, "")
}
