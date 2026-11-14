package domain

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"mikan/internal/panel/events"
	"mikan/internal/panel/events/feed"
	"mikan/internal/panel/store/db"
)

// A term that waits for its first connection: a subscription made on a tariff that says so
// (TariffLifecycle.StartOnConnect), or one the admin made wait, starts its term when its
// first traffic is counted, not when it was made. Until then its clock stands still (the
// housekeeping moves it on, SlideWaiting): the subscription shows its whole term, the nodes
// let it in like any other, and whatever of it would run out with time (the traffic period,
// packages with an end, places bought until the term ends) waits too.

// waitForConnection makes u's term wait for its first connection, on q's transaction:
// whatever of it is left now is what it will have then.
func waitForConnection(ctx context.Context, q *db.Queries, userID int64, now time.Time) error {
	return q.AddUserStart(ctx, db.AddUserStartParams{UserID: userID, FrozenAt: now.Unix()})
}

// mayWait refuses a wait that could never start or never mattered: a subscription that has
// connected already, one without an end, and one whose term is over (the nodes turn it away,
// so it would never connect).
func mayWait(u db.User, now time.Time) error {
	switch {
	case u.OnlineAt.Valid || u.TotalUp+u.TotalDown > 0:
		return fieldErr("starts_on_connect", "already_connected")
	case !u.ExpiresAt.Valid:
		return fieldErr("starts_on_connect", "no_term_end")
	case u.ExpiresAt.Int64 <= now.Unix():
		return fieldErr("starts_on_connect", "term_ended")
	}
	return nil
}

// Waiting says whether u's term waits for its first connection.
func Waiting(ctx context.Context, q *db.Queries, userID int64) (bool, error) {
	_, err := q.GetUserStart(ctx, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// WaitingOf is which of the users of ids wait for their first connection: a page's worth in
// one read.
func WaitingOf(ctx context.Context, q *db.Queries, ids []int64) (map[int64]bool, error) {
	rows, err := q.ListUserStartsOf(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]bool, len(rows))
	for _, r := range rows {
		out[r.UserID] = true
	}
	return out, nil
}

// slide moves the clocks of the waiting users of rows on to now (unix seconds), each by the
// time since its own last stop.
func slide(ctx context.Context, q *db.Queries, rows []db.UserStart, now int64) error {
	var terms db.SlideUserTermsParams
	var grants db.SlideUserGrantsParams
	for _, r := range rows {
		span := now - r.FrozenAt
		if span <= 0 {
			continue
		}
		terms.Ids, terms.Spans = append(terms.Ids, r.UserID), append(terms.Spans, span)
		grants.Ids, grants.Spans, grants.Frozen = append(grants.Ids, r.UserID), append(grants.Spans, span), append(grants.Frozen, r.FrozenAt)
	}
	if len(terms.Ids) == 0 {
		return nil
	}
	if err := q.SlideUserTerms(ctx, terms); err != nil {
		return err
	}
	return q.SlideUserGrants(ctx, grants)
}

// SlideWaiting moves the clock of every waiting subscription on to now, so that its expiry
// is as far ahead as when it began to wait: the housekeeping's round, every ten minutes. A
// wait with no term left to wait with (the term taken back by a refund, the end taken away)
// ends here, untold: whatever took the term said so itself. It returns how many wait.
//
// The users are locked in id order first, as the traffic batches lock them, which start the
// waits they see traffic for: the two take turns per user. READ COMMITTED: what the locks
// hold is read again after them.
func (s *Users) SlideWaiting(ctx context.Context) (int, error) {
	now := s.now().Unix()
	n := 0
	err := s.st.TxRC(ctx, func(q *db.Queries) error {
		n = 0
		rows, err := q.ListUserStarts(ctx)
		if err != nil || len(rows) == 0 {
			return err
		}
		ids := make([]int64, len(rows))
		for i, r := range rows {
			ids[i] = r.UserID
		}
		users, err := q.LockUserRows(ctx, ids)
		if err != nil || len(users) == 0 {
			return err
		}
		ends := make(map[int64]sql.NullInt64, len(users))
		locked := make([]int64, len(users))
		for i, u := range users {
			ends[u.ID], locked[i] = u.ExpiresAt, u.ID
		}
		if rows, err = q.ListUserStartsOf(ctx, locked); err != nil {
			return err
		}
		var keep, done []int64
		var waiting []db.UserStart
		for _, r := range rows {
			if end := ends[r.UserID]; !end.Valid || end.Int64 <= r.FrozenAt {
				done = append(done, r.UserID)
				continue
			}
			keep, waiting = append(keep, r.UserID), append(waiting, r)
		}
		if len(done) > 0 {
			if _, err := q.TakeUserStarts(ctx, done); err != nil {
				return err
			}
		}
		if err := slide(ctx, q, waiting, now); err != nil {
			return err
		}
		n = len(keep)
		return q.FreezeUserStarts(ctx, db.FreezeUserStartsParams{Now: now, Ids: keep})
	})
	return n, err
}

// startWaiting starts now the waiting terms of the users of ids, on q's transaction with
// those users locked: each clock moves on to now and runs from there, and the addons are told
// what started it (by: events.StartedByConnection, events.StartedByAdmin). It returns how
// many started.
func startWaiting(ctx context.Context, q *db.Queries, ids []int64, now time.Time, by string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	rows, err := q.TakeUserStarts(ctx, ids)
	if err != nil || len(rows) == 0 {
		return 0, err
	}
	starts := make([]db.UserStart, len(rows))
	for i, r := range rows {
		starts[i] = db.UserStart{UserID: r.UserID, FrozenAt: r.FrozenAt}
	}
	if err := slide(ctx, q, starts, now.Unix()); err != nil {
		return 0, err
	}
	for _, r := range starts {
		if err := toldStarted(ctx, q, r.UserID, now, by); err != nil {
			return 0, err
		}
	}
	return len(starts), nil
}

// endWait ends the wait of userID without moving its clock, on q's transaction: the admin
// gave the term an end of their own, which stands as it was given.
func endWait(ctx context.Context, q *db.Queries, userID int64, now time.Time) error {
	rows, err := q.TakeUserStarts(ctx, []int64{userID})
	if err != nil || len(rows) == 0 {
		return err
	}
	return toldStarted(ctx, q, userID, now, events.StartedByAdmin)
}

// toldStarted tells the addons userID's term runs now, in the transaction that started it.
// The wait's row goes in the same transaction, so a start cannot be told twice: no dedupe
// key, and a subscription the admin makes wait again is told again when that wait ends.
func toldStarted(ctx context.Context, q *db.Queries, userID int64, now time.Time, by string) error {
	u, err := q.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	tg, err := linkedAccount(ctx, q, userID)
	if err != nil {
		return err
	}
	return feed.Emit(ctx, q, now, events.UserStarted, userID, tg, events.Started{ExpiresAt: u.ExpiresAt.Int64, By: by}, "")
}

// linkedAccount is the Telegram account userID is linked to; 0: none.
func linkedAccount(ctx context.Context, q *db.Queries, userID int64) (int64, error) {
	l, err := q.GetTgLink(ctx, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return l.TgID, err
}

// EndWaitOn ends userID's wait on q's transaction, untold, for a caller that takes its term
// away (a refund of the payment that made it): with nothing left there is nothing to start,
// and the refund tells the addons itself.
func EndWaitOn(ctx context.Context, q *db.Queries, userID int64) error {
	_, err := q.TakeUserStarts(ctx, []int64{userID})
	return err
}
