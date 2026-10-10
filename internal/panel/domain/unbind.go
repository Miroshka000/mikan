package domain

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

// UnbindRules is how the subscriber may free places: so many unbinds a period, and how
// long a device they unbound may not take a place again. Without a limit a reseller would
// let buyers in one after another; without the pause an app still open on the unbound
// device fetches the subscription and binds again, and the unbind seems to do nothing.
// The admin unbinds outside these rules.
type UnbindRules struct {
	// Limit is how many devices the subscriber may unbind in Days; 0: as many as they like.
	Limit int `json:"limit" minimum:"0" maximum:"100" doc:"Сколько устройств подписчик может отвязать за days; 0 — без ограничения"`
	// Days is the window the limit counts in, sliding: an unbind leaves it Days later.
	Days int `json:"days" minimum:"1" maximum:"365" doc:"За сколько дней считается лимит (скользящее окно)"`
	// ReturnHours is how long a device the subscriber unbound may not bind again; 0: at
	// once. The admin sees it among the bans and may let it in earlier.
	ReturnHours int `json:"return_hours" minimum:"0" maximum:"720" doc:"Сколько часов отвязанное подписчиком устройство не может привязаться снова; 0 — сразу"`
}

// DefaultUnbindRules: one a day, and the unbound device stays out for a day, until the
// admin says otherwise.
var DefaultUnbindRules = UnbindRules{Limit: 1, Days: 1, ReturnHours: 24}

const (
	MaxUnbindLimit = 100
	MaxUnbindDays  = 365
	MaxReturnHours = 30 * 24
)

// Validate checks rules the admin sends.
func (r UnbindRules) Validate() error {
	switch {
	case r.Limit < 0 || r.Limit > MaxUnbindLimit:
		return fieldErr("limit", "bad_unbind_limit")
	case r.Days < 1 || r.Days > MaxUnbindDays:
		return fieldErr("days", "bad_unbind_days")
	case r.ReturnHours < 0 || r.ReturnHours > MaxReturnHours:
		return fieldErr("return_hours", "bad_return_hours")
	}
	return nil
}

func (r UnbindRules) window() time.Duration { return time.Duration(r.Days) * 24 * time.Hour }

// LoadUnbindRules reads the admin's rules; DefaultUnbindRules until they set any. Rules
// stored broken (by hand) fall back to the default rather than lock unbinding up.
func LoadUnbindRules(ctx context.Context, set *settings.Settings) (UnbindRules, error) {
	r, _, err := settings.GetOver(ctx, set, settings.KeyUnbindRules, DefaultUnbindRules)
	if err != nil {
		return DefaultUnbindRules, err
	}
	if r.Validate() != nil {
		return DefaultUnbindRules, nil
	}
	return r, nil
}

// UnbindState is what the subscriber may do now under the rules.
type UnbindState struct {
	Rules UnbindRules
	// Used is how many unbinds of theirs the window holds.
	Used int
	// Next is when they may unbind again; zero: now.
	Next time.Time
}

// Left is how many unbinds the window still has room for; -1 without a limit.
func (s UnbindState) Left() int {
	if s.Rules.Limit == 0 {
		return -1
	}
	return max(0, s.Rules.Limit-s.Used)
}

// ErrDeviceUnbound: the subscriber unbound this device a moment ago; it may bind again
// at UnboundError.Until.
var ErrDeviceUnbound = errors.New("device_unbound")

type UnboundError struct{ Until time.Time }

func (e *UnboundError) Error() string {
	return fmt.Sprintf("device_unbound until %s", e.Until.UTC().Format(time.RFC3339))
}
func (e *UnboundError) Is(target error) bool { return target == ErrDeviceUnbound }

// UnbindState of userID now.
func (d *Devices) UnbindState(ctx context.Context, userID int64) (UnbindState, error) {
	rules, err := LoadUnbindRules(ctx, d.set)
	if err != nil {
		return UnbindState{Rules: rules}, err
	}
	return unbindState(ctx, d.st.Q, rules, userID, d.now())
}

// unbindState counts the unbinds in the window: with Limit of them there, the next comes
// when the oldest that fills it leaves the window.
func unbindState(ctx context.Context, q *db.Queries, rules UnbindRules, userID int64, now time.Time) (UnbindState, error) {
	st := UnbindState{Rules: rules}
	if rules.Limit == 0 {
		return st, nil
	}
	ats, err := q.ListUnbindsSince(ctx, db.ListUnbindsSinceParams{UserID: userID, At: now.Add(-rules.window()).Unix()})
	if err != nil {
		return st, err
	}
	st.Used = len(ats)
	if st.Used >= rules.Limit {
		st.Next = time.Unix(ats[st.Used-rules.Limit], 0).Add(rules.window()).UTC()
	}
	return st, nil
}

// holdUnbound keeps a device the subscriber unbound out for the rules' pause, as a ban
// that ends by itself; an admin's ban of it stays.
func holdUnbound(ctx context.Context, q *db.Queries, rules UnbindRules, userID int64, dev db.BoundDevice, now time.Time) error {
	if dev.Hwid == "" || rules.ReturnHours == 0 {
		return nil
	}
	until := now.Add(time.Duration(rules.ReturnHours) * time.Hour).Unix()
	return q.HoldUnbound(ctx, db.HoldUnboundParams{UserID: userID, Hwid: dev.Hwid, Label: DeviceLabel(dev), BannedAt: now.Unix(),
		Until: sql.NullInt64{Int64: until, Valid: true}})
}

// unbindKeep is how long unbinds are kept: the longest window there may be.
const unbindKeep = (MaxUnbindDays + 1) * 24 * time.Hour

// Prune drops the pauses that have ended and the unbinds no window reaches any more.
func (d *Devices) Prune(ctx context.Context) error {
	now := d.now()
	if err := d.st.Q.DeleteExpiredBans(ctx, sql.NullInt64{Int64: now.Unix(), Valid: true}); err != nil {
		return err
	}
	return d.st.Q.DeleteUnbindsBefore(ctx, now.Add(-unbindKeep).Unix())
}
