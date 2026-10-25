// Package domain holds the business rules for users, tariffs and the slot pool.
package domain

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"mikan/internal/panel/events"
	"mikan/internal/panel/events/feed"
	"mikan/internal/panel/model"
	"mikan/internal/panel/secure"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

// The states of a user (State).
const (
	StateActive   = model.StateActive
	StateExpiring = model.StateExpiring
	StateLimited  = model.StateLimited
	StateExpired  = model.StateExpired
	StateDisabled = model.StateDisabled
)

const day = int64(24 * time.Hour / time.Second)

var (
	ErrNotFound = model.ErrNotFound
	ErrNoSlots  = errors.New("no free slots")
)

// State derives what the user can do right now; limited and expired are never stored.
// grants is what is left of the user's active main grants (GrantsLeft.Main): a user past
// the base quota with grants left is not limited. The rule is the model's: the bot addon
// computes the same.
func State(u db.User, grants int64, now time.Time) string {
	return model.State(model.User(u), grants, now)
}

func CanConnect(state string) bool { return state == StateActive || state == StateExpiring }

// CountStates counts the users in each State in the database, without loading them.
func CountStates(ctx context.Context, q *db.Queries, now time.Time) (db.CountUserStatesRow, error) {
	return q.CountUserStates(ctx, db.CountUserStatesParams{Now: now.Unix(), ExpiringWithin: int64(model.ExpiringWindow / time.Second)})
}

// NextReset is when the traffic counter of the current period drops to zero (model.NextReset).
func NextReset(u db.User, now time.Time) (time.Time, bool) {
	return model.NextReset(model.User(u), now)
}

// Changes tells the node syncer what to push after a mutation.
type Changes interface {
	PoliciesChanged()
	SlotsChanged()
}

type Users struct {
	st      *store.Store
	set     *settings.Settings
	now     func() time.Time
	pool    *Pool
	changes Changes
	hub     *events.Hub // woken after a user is made; nil: no one waits
}

// SetEvents connects the hub that wakes the addons reading the feed (events.UserCreated is
// written in the transaction that makes the user).
func (s *Users) SetEvents(h *events.Hub) { s.hub = h }

func NewUsers(st *store.Store, pool *Pool, changes Changes, now func() time.Time) *Users {
	return &Users{st: st, set: settings.New(st.Q), now: now, pool: pool, changes: changes}
}

// Where a user came from, kept for the admin's list (users.source). It is a record, not a
// permission: nothing reads it to decide what a user may do.
const (
	UserFromAdmin  = "admin"  // made in the panel or by an API key
	UserFromBot    = "bot"    // bought in the Telegram bot
	UserFromTrial  = "trial"  // the bot's free trial
	UserFromImport = "import" // brought over from another panel
	UserFromFree   = "free"   // the free plan, taken in the bot or the Mini App
)

// UserOrigins lists them in the order the list shows them.
var UserOrigins = []string{UserFromAdmin, UserFromBot, UserFromTrial, UserFromFree, UserFromImport}

// ValidOrigin tells a source the database accepts.
func ValidOrigin(s string) bool { return slices.Contains(UserOrigins, s) }

type CreateInput struct {
	Name     string
	Contact  string
	Note     string
	Tags     []string
	TariffID int64
	// TermDays is the term bought (a payment's); invalid: the tariff's own.
	TermDays sql.NullInt64
	// Source is where the user comes from; empty: UserFromAdmin. Every path that makes
	// users names its own.
	Source string
	// TgID is the Telegram account that buys or takes the user, 0 for the admin's: it goes
	// to the addons with events.UserCreated, nothing else reads it.
	TgID int64
	// StartsOnConnect makes the term wait for the first connection (starts.go); nil: as the
	// tariff says (TariffLifecycle.StartOnConnect). A term without an end waits for nothing,
	// and an import's users bring theirs running.
	StartsOnConnect *bool
}

// Create makes a user the admin names (the panel, an API key): the name is checked as
// CleanUserName does.
func (s *Users) Create(ctx context.Context, in CreateInput) (db.User, error) {
	name, err := CleanUserName(in.Name)
	if err != nil {
		return db.User{}, err
	}
	in.Name = name
	var u db.User
	err = s.WithSlots(ctx, func() (err error) {
		u, err = s.create(ctx, in)
		return err
	})
	if err != nil {
		return db.User{}, err
	}
	s.hub.Kick()
	s.changes.PoliciesChanged()
	return u, nil
}

func (s *Users) create(ctx context.Context, in CreateInput) (db.User, error) {
	var u db.User
	err := s.st.Tx(ctx, func(q *db.Queries) error {
		var err error
		u, err = s.createTx(ctx, q, in, false)
		return err
	})
	return u, err
}

// createTx makes a user on q's transaction; anyTariff also takes an archived tariff (one
// that was paid for before the admin archived it).
func (s *Users) createTx(ctx context.Context, q *db.Queries, in CreateInput, anyTariff bool) (db.User, error) {
	now := s.now().Unix()
	tags, err := encodeTags(in.Tags)
	if err != nil {
		return db.User{}, err
	}
	source := cmp.Or(in.Source, UserFromAdmin)
	if !ValidOrigin(source) {
		return db.User{}, fieldErr("source", "bad_source")
	}
	t, err := q.GetTariff(ctx, in.TariffID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && t.Archived != 0 && !anyTariff) {
		return db.User{}, fmt.Errorf("tariff %d: %w", in.TariffID, ErrNotFound)
	}
	if err != nil {
		return db.User{}, err
	}
	slot, err := q.TakeFreeSlot(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return db.User{}, ErrNoSlots
	}
	if err != nil {
		return db.User{}, err
	}
	u, err := q.CreateUser(ctx, db.CreateUserParams{
		Name: strings.TrimSpace(in.Name), Contact: strings.TrimSpace(in.Contact), Note: in.Note, Tags: tags,
		TariffID: sql.NullInt64{Int64: t.ID, Valid: true}, TrafficLimit: t.TrafficLimit, DeviceLimit: t.DeviceLimit, SpeedLimit: t.SpeedLimit,
		ResetStrategy: t.ResetStrategy, PeriodDays: 30, PeriodStart: now,
		ExpiresAt:  tariffExpiry(time.Unix(now, 0), durationTariff{termDays(t, in.TermDays), t.BillingDay}),
		BillingDay: t.BillingDay, SubToken: secure.Token(24), SlotID: sql.NullInt64{Int64: slot.ID, Valid: true}, CreatedAt: now, UpdatedAt: now,
		Source: source,
	})
	if err != nil {
		return u, err
	}
	if err := setDeviceMode(ctx, q, &u, t.DeviceMode, now); err != nil {
		return u, err
	}
	if err := ApplyTariffPools(ctx, q, u.ID, t.ID); err != nil {
		return u, err
	}
	// Told in the same transaction, never of a user that rolled back. An import is a bulk move
	// of people who already have their subscriptions: no one to welcome, and thousands of events.
	if source == UserFromImport {
		return u, nil
	}
	wait := in.StartsOnConnect
	if wait == nil {
		tl, err := LoadTariffLifecycle(ctx, q, t.ID)
		if err != nil {
			return u, err
		}
		wait = &tl.StartOnConnect
	}
	if *wait && u.ExpiresAt.Valid {
		if err := waitForConnection(ctx, q, u.ID, time.Unix(now, 0)); err != nil {
			return u, err
		}
	}
	return u, feed.Emit(ctx, q, s.now(), events.UserCreated, u.ID, in.TgID, events.User{Source: source, TariffID: t.ID},
		events.UserCreated+":"+strconv.FormatInt(u.ID, 10))
}

// PurchaseInput is what a payment buys.
type PurchaseInput struct {
	// UserID is the subscription a renewal extends; 0, or one deleted since the invoice,
	// makes a new subscription named Name.
	UserID   int64
	TariffID int64
	// Term is the term bought, as it was when bought; invalid: the tariff's own.
	Term sql.NullInt64
	Name string
	// Note and Source go on a new subscription; empty: bought in the bot. A payment of a
	// kind of its own names itself (the trial, bought for a price).
	Note   string
	Source string
	// TgID is the Telegram account that pays (for events.UserCreated).
	TgID int64
	// ResetTraffic: a renewal also starts a new traffic period (the payment buys a full
	// quota); off, the counter keeps running.
	ResetTraffic bool
	// KeepExtraDevices: devices bought on top of the tariff stay with a renewal of the same
	// tariff; off, or another tariff, they go with the term they were bought for.
	KeepExtraDevices bool
	// PaymentID is the payment that pays (0: none): what it carries over goes with it on a
	// refund.
	PaymentID int64
}

// Purchase applies a paid tariff on q's transaction, so the payment and its effect commit
// together. in.UserID 0 (or a user deleted since the invoice) makes a new subscription
// named in.Name. A renewal takes the tariff's limits, adds its term after the current one
// (from now when that already ended; what is left of another tariff's term is what it is
// worth on this one, carryOver) and turns the user on, one the admin switched off too:
// the buyer paid for the term, and the admin who wants someone out deletes the subscription
// or stops selling to them (decided 2026-10-11). A renewal that starts a new traffic period
// carries what the old one left into it, by the carry-over rules (carryInto).
// ErrNoSlots: refill and run again. The caller calls Changed after the commit.
func (s *Users) Purchase(ctx context.Context, q *db.Queries, in PurchaseInput) (u db.User, created bool, err error) {
	if in.UserID != 0 {
		u, err = q.GetUser(ctx, in.UserID)
	}
	if in.UserID == 0 || errors.Is(err, sql.ErrNoRows) {
		u, err = s.createTx(ctx, q, CreateInput{Name: in.Name, Note: cmp.Or(in.Note, "Telegram"), TariffID: in.TariffID,
			TermDays: in.Term, Source: cmp.Or(in.Source, UserFromBot), TgID: in.TgID}, true)
		return u, err == nil, err
	}
	if err != nil {
		return u, false, err
	}
	t, err := q.GetTariff(ctx, in.TariffID)
	if err != nil {
		return u, false, err
	}
	now := s.now()
	base := now
	if u.ExpiresAt.Valid && store.Time(u.ExpiresAt.Int64).After(now) {
		left := store.Time(u.ExpiresAt.Int64).Sub(now)
		// Another tariff: what is left is worth its price there (carryOver).
		if u.TariffID.Valid && u.TariffID.Int64 != t.ID {
			if left, err = carryOver(ctx, q, u.TariffID.Int64, t, termDays(t, in.Term), left); err != nil {
				return u, false, err
			}
		}
		base = now.Add(left)
	}
	// What the period leaves is counted on the user as it is, before the tariff's limit.
	var carried int64
	if in.ResetTraffic {
		if carried, err = carryInto(ctx, q, u, t); err != nil {
			return u, false, err
		}
	}
	keep := in.KeepExtraDevices && u.TariffID.Valid && u.TariffID.Int64 == t.ID
	if u, err = takeTariff(ctx, q, u, t, tariffTake{base: base, days: termDays(t, in.Term), keepExtra: keep, turnOn: true}, now); err != nil {
		return u, false, err
	}
	if !in.ResetTraffic {
		return u, false, nil
	}
	if err := StartPeriod(ctx, q, u.ID, now.Unix(), now); err != nil {
		return u, false, err
	}
	if u, err = q.GetUser(ctx, u.ID); err != nil {
		return u, false, err
	}
	return u, false, grantCarry(ctx, q, u, carried, in.PaymentID, now)
}

// tariffTake is how takeTariff puts a user on a tariff.
type tariffTake struct {
	base time.Time // where the new term starts
	days int64     // the term on the tariff (termDays)
	// keepExtra: places bought on top stay with the term (a renewal of the same tariff that
	// pays for them again, billing.renewalExtra).
	keepExtra bool
	// turnOn: the subscription is turned on, one the admin switched off too (a payment's).
	turnOn bool
}

// takeTariff puts u on tariff t as of now, on q's transaction: the tariff's limits, device
// mode and server groups, a term of take.days from take.base up to the tariff's billing day
// (the user's own without one), and the places bought on top: with keepExtra they go on;
// otherwise those bought until a date still ahead stay until then and not into the new term
// (the maintenance takes them back, Devices.ExpireExtra), the rest go now. The traffic
// period is the caller's.
func takeTariff(ctx context.Context, q *db.Queries, u db.User, t db.Tariff, take tariffTake, now time.Time) (db.User, error) {
	billingDay := t.BillingDay
	if !billingDay.Valid {
		billingDay = u.BillingDay
	}
	devices, extra, until := t.DeviceLimit, int64(0), sql.NullInt64{}
	switch {
	case take.keepExtra && u.ExtraDevices > 0:
		extra = u.ExtraDevices
	case u.ExtraDevices > 0 && u.ExtraDevicesUntil.Valid && u.ExtraDevicesUntil.Int64 > now.Unix():
		extra, until = u.ExtraDevices, u.ExtraDevicesUntil
	}
	if devices.Valid {
		devices.Int64 += extra
	}
	if extra != u.ExtraDevices || until != u.ExtraDevicesUntil {
		if err := q.SetUserExtraDevices(ctx, db.SetUserExtraDevicesParams{ExtraDevices: extra, Until: until, ID: u.ID}); err != nil {
			return u, err
		}
	}
	par := UserParams(u, now.Unix())
	if take.turnOn {
		par.Status = "active"
	}
	par.TariffID = sql.NullInt64{Int64: t.ID, Valid: true}
	par.TrafficLimit, par.DeviceLimit, par.SpeedLimit, par.ResetStrategy = t.TrafficLimit, devices, t.SpeedLimit, t.ResetStrategy
	par.ExpiresAt, par.BillingDay = tariffExpiry(take.base, durationTariff{take.days, billingDay}), billingDay
	u, err := q.UpdateUser(ctx, par)
	if err != nil {
		return u, err
	}
	if err := setDeviceMode(ctx, q, &u, t.DeviceMode, now.Unix()); err != nil {
		return u, err
	}
	return u, ApplyTariffPools(ctx, q, u.ID, t.ID)
}

// UserParams is u as it is, to change some of it and write it back with UpdateUser: the
// one copy of the user's columns, so a column added to UpdateUser is not written as zero by
// a caller that forgot it.
func UserParams(u db.User, now int64) db.UpdateUserParams {
	return db.UpdateUserParams{Name: u.Name, Contact: u.Contact, Note: u.Note, Tags: u.Tags, Status: u.Status, TariffID: u.TariffID,
		TrafficLimit: u.TrafficLimit, DeviceLimit: u.DeviceLimit, SpeedLimit: u.SpeedLimit, ResetStrategy: u.ResetStrategy, PeriodDays: u.PeriodDays, PeriodStart: u.PeriodStart,
		ExpiresAt: u.ExpiresAt, Inbounds: u.Inbounds, BillingDay: u.BillingDay, UpdatedAt: now, ID: u.ID}
}

// RefillSlots tops the slot pool up after ErrNoSlots.
func (s *Users) RefillSlots(ctx context.Context) error {
	return refill(ctx, s.pool, s.changes)
}

// WithSlots runs fn, and once more after the pool is topped up when it ran out of slots
// (ErrNoSlots): what needs a free slot (a new user, a reissue, a device) takes one either way.
func (s *Users) WithSlots(ctx context.Context, fn func() error) error {
	return withSlots(ctx, s.pool, s.changes, fn)
}

func withSlots(ctx context.Context, pool *Pool, changes Changes, fn func() error) error {
	err := fn()
	if errors.Is(err, ErrNoSlots) {
		if err := refill(ctx, pool, changes); err != nil {
			return err
		}
		err = fn()
	}
	return err
}

// refill tops the pool up and tells the nodes of the new slots.
func refill(ctx context.Context, pool *Pool, changes Changes) error {
	if err := pool.Refill(ctx, RefillBatch); err != nil {
		return err
	}
	changes.SlotsChanged()
	return nil
}

// CreateOn makes a user on q's transaction and applies p on top of the tariff's limits, so
// a user is there whole or not at all (an import: the old panel's limit, term and status
// come with the user). ErrNoSlots: RefillSlots, then run again. The caller calls Changed
// after the commit.
func (s *Users) CreateOn(ctx context.Context, q *db.Queries, in CreateInput, p Patch) (db.User, error) {
	u, err := s.createTx(ctx, q, in, false)
	if err != nil {
		return u, err
	}
	return s.updateOn(ctx, q, u.ID, p)
}

// RefillFor makes sure n users can be made without running out of slots: one refill for
// what is missing, one word to the nodes (an import, before its users).
func (s *Users) RefillFor(ctx context.Context, n int) error {
	ps, err := s.pool.Stats(ctx)
	if err != nil {
		return err
	}
	missing := int64(n) - ps.Free
	if missing <= 0 {
		return nil
	}
	if err := s.pool.Refill(ctx, int(missing)); err != nil {
		return err
	}
	s.changes.SlotsChanged()
	return nil
}

// Changed tells the nodes about users changed on a transaction of the caller's.
func (s *Users) Changed() { s.changes.PoliciesChanged() }

func expiry(from, days int64) sql.NullInt64 {
	if days <= 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: store.ClampUnix(from + days*day), Valid: true}
}

func (s *Users) Get(ctx context.Context, id int64) (db.User, error) {
	u, err := s.st.Q.GetUser(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

// Patch is a partial update; nil fields are left unchanged. ClearX removes a limit.
type Patch struct {
	Name, Contact, Note *string
	Tags                *[]string
	Disabled            *bool
	TrafficLimit        *int64
	ClearTrafficLimit   bool
	DeviceLimit         *int64
	ClearDeviceLimit    bool
	SpeedLimit          *int64 // Mbit/s each way
	ClearSpeedLimit     bool
	ExpiresAt           *time.Time
	ClearExpiry         bool
	BillingDay          *int64 // 1–31: terms end on that day of the month
	ClearBillingDay     bool
	Inbounds            *[]int64 // empty slice = all inbounds
	TariffID            *int64   // applies the tariff's limits and restarts the term from now
	// Hidden takes the user off the admin's list (or back on it). It changes nothing else:
	// the subscription, the billing and the nodes see the same user.
	Hidden *bool
	// DeviceMode changes how the device limit counts (DeviceModeBound, DeviceModeOnline);
	// a tariff in the same patch sets it first, and this one wins.
	DeviceMode *string
	// FolderID puts the user into a folder; ClearFolder takes the user out of theirs.
	FolderID    *int64
	ClearFolder bool
	// Extend adds a term to the expiry the transaction reads, so a payment that lands
	// meanwhile is not overwritten by an absolute date worked out before it.
	Extend *Extension
	// StartsOnConnect true makes the term wait for the first connection (starts.go): only
	// a subscription that never connected and whose term is still ahead; false starts a
	// waiting term now. An end given by hand (ExpiresAt, ClearExpiry) ends a wait too and
	// stands as given; an extension adds to what waits.
	StartsOnConnect *bool
}

// Extension adds time to the current expiry, or to now when the term already ended. Days
// adds that many; Months goes to the n-th billing day, or without one to the same day of
// the month (see AddMonths); Period is one paid period: a month up to the billing day, or
// 30 days without one. It also turns the user on.
type Extension struct {
	Days   int64
	Months int
	Period bool
}

// until is where the term ends after the extension, counted from the expiry exp.
func (e Extension) until(now time.Time, exp sql.NullInt64, billingDay sql.NullInt64) time.Time {
	base := now
	if exp.Valid {
		t := store.Time(exp.Int64)
		if t.After(now) {
			base = t
		}
	}
	var res time.Time
	switch {
	case e.Period && billingDay.Valid:
		res = AddMonths(base, 1, billingDay)
	case e.Period:
		res = base.Add(30 * 24 * time.Hour)
	case e.Months > 0:
		res = AddMonths(base, e.Months, billingDay)
	default:
		res = base.Add(time.Duration(e.Days) * 24 * time.Hour)
	}
	res = store.ClampTime(res)
	return res
}

// ErrBadBillingDay: a billing day is 1–31.
var ErrBadBillingDay = errors.New("bad_billing_day")

// MaxSpeedLimit bounds a speed cap: 100 Gbit/s, past any server's port.
const MaxSpeedLimit = 100_000

// ErrBadSpeedLimit: a speed cap is 1–MaxSpeedLimit Mbit/s.
var ErrBadSpeedLimit = errors.New("bad_speed_limit")

// ValidSpeedLimit says whether mbps is a speed cap the panel takes.
func ValidSpeedLimit(mbps int64) bool { return mbps >= 1 && mbps <= MaxSpeedLimit }

func (s *Users) Update(ctx context.Context, id int64, p Patch) (db.User, error) {
	var out db.User
	err := s.st.Tx(ctx, func(q *db.Queries) (err error) {
		out, err = s.updateOn(ctx, q, id, p)
		return err
	})
	if err == nil {
		// A wait that ended is told (events.UserStarted).
		if p.StartsOnConnect != nil || p.ExpiresAt != nil || p.ClearExpiry {
			s.hub.Kick()
		}
		s.changes.PoliciesChanged()
	}
	return out, err
}

// updateOn applies a patch on q's transaction; the caller tells the nodes.
func (s *Users) updateOn(ctx context.Context, q *db.Queries, id int64, p Patch) (db.User, error) {
	// The folder is locked before the user's row, as a deleted folder locks its own first
	// and then its users': the two never wait for each other in a circle.
	if p.FolderID != nil {
		if err := lockFolder(ctx, q, *p.FolderID); err != nil {
			return db.User{}, err
		}
	}
	u, err := q.GetUser(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return db.User{}, ErrNotFound
	}
	if err != nil {
		return db.User{}, err
	}
	now := s.now().Unix()
	par := UserParams(u, now)
	mode := u.DeviceMode
	if p.TariffID != nil {
		t, err := q.GetTariff(ctx, *p.TariffID)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && t.Archived != 0) {
			return db.User{}, fmt.Errorf("tariff %d: %w", *p.TariffID, ErrNotFound)
		}
		if err != nil {
			return db.User{}, err
		}
		par.TariffID = sql.NullInt64{Int64: t.ID, Valid: true}
		par.TrafficLimit, par.DeviceLimit, par.ResetStrategy, par.BillingDay = t.TrafficLimit, t.DeviceLimit, t.ResetStrategy, t.BillingDay
		par.SpeedLimit, mode = t.SpeedLimit, t.DeviceMode
		par.ExpiresAt = tariffExpiry(time.Unix(now, 0), durationTariff{t.DurationDays, t.BillingDay})
		if err := ApplyTariffPools(ctx, q, u.ID, t.ID); err != nil {
			return db.User{}, err
		}
	}
	switch {
	case p.ClearBillingDay:
		par.BillingDay = sql.NullInt64{}
	case p.BillingDay != nil:
		if !ValidBillingDay(*p.BillingDay) {
			return db.User{}, ErrBadBillingDay
		}
		par.BillingDay = NullInt(p.BillingDay)
	}
	if p.Name != nil {
		// A rename changes only the name: the link, the slots and the keys stay.
		if par.Name, err = CleanUserName(*p.Name); err != nil {
			return db.User{}, err
		}
	}
	if p.Contact != nil {
		par.Contact = strings.TrimSpace(*p.Contact)
	}
	if p.Note != nil {
		par.Note = *p.Note
	}
	if p.Tags != nil {
		if par.Tags, err = encodeTags(*p.Tags); err != nil {
			return db.User{}, err
		}
	}
	if p.Disabled != nil {
		par.Status = "active"
		if *p.Disabled {
			par.Status = "disabled"
		}
	}
	switch {
	case p.ClearTrafficLimit:
		par.TrafficLimit = sql.NullInt64{}
	case p.TrafficLimit != nil:
		par.TrafficLimit = NullInt(p.TrafficLimit)
	}
	switch {
	case p.ClearDeviceLimit:
		par.DeviceLimit = sql.NullInt64{}
	case p.DeviceLimit != nil:
		par.DeviceLimit = NullInt(p.DeviceLimit)
	}
	if p.DeviceMode != nil {
		if !ValidDeviceMode(*p.DeviceMode) {
			return db.User{}, fieldErr("device_mode", "bad_device_mode")
		}
		mode = *p.DeviceMode
	}
	switch {
	case p.ClearSpeedLimit:
		par.SpeedLimit = sql.NullInt64{}
	case p.SpeedLimit != nil:
		if !ValidSpeedLimit(*p.SpeedLimit) {
			return db.User{}, ErrBadSpeedLimit
		}
		par.SpeedLimit = NullInt(p.SpeedLimit)
	}
	switch {
	case p.ClearExpiry:
		par.ExpiresAt = sql.NullInt64{}
	case p.ExpiresAt != nil:
		sec := store.ClampUnix(p.ExpiresAt.Unix())
		par.ExpiresAt = sql.NullInt64{Int64: sec, Valid: true}
	}
	if p.Extend != nil {
		if p.Extend.Days < 0 || p.Extend.Days > MaxExtendDays {
			return db.User{}, fieldErr("days", "bad_days")
		}
		res := p.Extend.until(s.now(), par.ExpiresAt, par.BillingDay)
		sec := store.ClampUnix(res.Unix())
		par.ExpiresAt = sql.NullInt64{Int64: sec, Valid: true}
		par.Status = "active"
	}
	if p.Inbounds != nil {
		if len(*p.Inbounds) == 0 {
			par.Inbounds = sql.NullString{}
		} else {
			raw, _ := json.Marshal(*p.Inbounds)
			par.Inbounds = sql.NullString{String: string(raw), Valid: true}
		}
	}
	out, err := q.UpdateUser(ctx, par)
	if err != nil {
		return out, err
	}
	if err := setDeviceMode(ctx, q, &out, mode, now); err != nil {
		return out, err
	}
	// Where the user is listed is not one of the fields above: it changes on its own
	// statements, and the row returned follows.
	if p.Hidden != nil {
		out.Hidden = Flag(*p.Hidden)
		if err := q.SetUserHidden(ctx, db.SetUserHiddenParams{Hidden: out.Hidden, UpdatedAt: now, ID: id}); err != nil {
			return out, err
		}
	}
	if p.FolderID != nil || p.ClearFolder {
		out.FolderID = sql.NullInt64{}
		if p.FolderID != nil && !p.ClearFolder {
			out.FolderID = NullInt(p.FolderID)
		}
		if err := q.SetUserFolder(ctx, db.SetUserFolderParams{FolderID: out.FolderID, UpdatedAt: now, ID: id}); err != nil {
			return out, err
		}
	}
	return s.waitOn(ctx, q, out, p)
}

// waitOn applies what p says of the wait for the first connection to out, just written on
// q's transaction, and returns the user as it is after it.
func (s *Users) waitOn(ctx context.Context, q *db.Queries, out db.User, p Patch) (db.User, error) {
	now := s.now()
	if p.ExpiresAt != nil || p.ClearExpiry {
		if err := endWait(ctx, q, out.ID, now); err != nil {
			return out, err
		}
	}
	if p.StartsOnConnect == nil {
		return out, nil
	}
	waiting, err := Waiting(ctx, q, out.ID)
	switch {
	case err != nil:
		return out, err
	case *p.StartsOnConnect && !waiting:
		if err := mayWait(out, now); err != nil {
			return out, err
		}
		return out, waitForConnection(ctx, q, out.ID, now)
	case !*p.StartsOnConnect && waiting:
		if _, err := startWaiting(ctx, q, []int64{out.ID}, now, events.StartedByAdmin); err != nil {
			return out, err
		}
		// The clock moved on to now: the row as it is after.
		return q.GetUser(ctx, out.ID)
	}
	return out, nil
}

// lockFolder keeps a folder from being deleted until the transaction ends; a folder that
// is not there is the caller's mistake, not a missing record.
func lockFolder(ctx context.Context, q *db.Queries, id int64) error {
	if _, err := q.LockFolder(ctx, id); errors.Is(err, sql.ErrNoRows) {
		return fieldErr("folder_id", "folder_not_found")
	} else if err != nil {
		return err
	}
	return nil
}

// Extend adds days to the current expiry, or to now if the term already ended.
// The three extensions read the user and write the new expiry in one transaction (Update).
func (s *Users) Extend(ctx context.Context, id int64, days int64) (db.User, error) {
	return s.Update(ctx, id, Patch{Extend: &Extension{Days: days}})
}

// MaxExtendDays bounds one extension by days: ten years. Every way to extend (Update's
// Patch, the bulk action, an addon's grant) is held to it here, not by its caller.
const MaxExtendDays = 3650

// ExtendOn is Extend on q's transaction, for a caller that writes more in it (an addon's
// grant with its key, its audit line and its event); the caller tells the nodes (Changed).
func (s *Users) ExtendOn(ctx context.Context, q *db.Queries, id, days int64) (db.User, error) {
	if days < 1 {
		return db.User{}, fieldErr("days", "bad_days")
	}
	return s.updateOn(ctx, q, id, Patch{Extend: &Extension{Days: days}})
}

// ExtendMonths adds n months: to the n-th billing day, or without one to the same day of
// the month (see AddMonths). A term that already ended restarts from now.
func (s *Users) ExtendMonths(ctx context.Context, id int64, n int) (db.User, error) {
	return s.Update(ctx, id, Patch{Extend: &Extension{Months: n}})
}

// ResetTraffic starts a new traffic period now (see StartPeriod).
func (s *Users) ResetTraffic(ctx context.Context, id int64) (db.User, error) {
	err := s.st.Tx(ctx, func(q *db.Queries) error { return s.resetOn(ctx, q, id) })
	if err != nil {
		return db.User{}, err
	}
	s.changes.PoliciesChanged()
	return s.Get(ctx, id)
}

func (s *Users) resetOn(ctx context.Context, q *db.Queries, id int64) error {
	if _, err := q.GetUser(ctx, id); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	now := s.now()
	return StartPeriod(ctx, q, id, now.Unix(), now)
}

// Reissue gives the user a new slot and subscription token. The old credentials stop
// working at once: the old slot is burned and stays denied until it is purged.
func (s *Users) Reissue(ctx context.Context, id int64) (db.User, error) {
	err := s.WithSlots(ctx, func() error { return s.reissue(ctx, id) })
	if err != nil {
		return db.User{}, err
	}
	s.hub.Kick() // the devices it unbound
	s.changes.PoliciesChanged()
	return s.Get(ctx, id)
}

func (s *Users) reissue(ctx context.Context, id int64) error {
	now := s.now().Unix()
	return s.st.Tx(ctx, func(q *db.Queries) error { return s.reissueOn(ctx, q, id, now) })
}

// reissueOn gives the user a new slot and token on q's transaction; the caller tells the nodes.
func (s *Users) reissueOn(ctx context.Context, q *db.Queries, id, now int64) error {
	u, err := q.GetUser(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	slot, err := q.TakeFreeSlot(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNoSlots
	}
	if err != nil {
		return err
	}
	if u.SlotID.Valid {
		if err := q.BurnSlot(ctx, db.BurnSlotParams{BurnedAt: sql.NullInt64{Int64: now, Valid: true}, ID: u.SlotID.Int64}); err != nil {
			return err
		}
	}
	// A new link: every bound device registers again with it.
	if err := burnDevices(ctx, q, id, now); err != nil {
		return err
	}
	// The old panel's links go too: a leaked one must not serve the new keys.
	if err := q.DeleteLegacySubTokensOf(ctx, id); err != nil {
		return err
	}
	return q.SetUserCredentials(ctx, db.SetUserCredentialsParams{SlotID: sql.NullInt64{Int64: slot.ID, Valid: true}, SubToken: secure.Token(24), UpdatedAt: now, ID: id})
}

func (s *Users) Delete(ctx context.Context, id int64) error {
	err := s.st.Tx(ctx, func(q *db.Queries) error { return s.deleteOn(ctx, q, id) })
	if err == nil {
		s.changes.PoliciesChanged()
	}
	return err
}

func (s *Users) deleteOn(ctx context.Context, q *db.Queries, id int64) error {
	now := s.now().Unix()
	u, err := q.GetUser(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return deleteUsers(ctx, q, []int64{u.ID}, now)
}

// deleteUsers deletes users on q's transaction. Their slots and those of their registered
// bound devices are burned: denied on the nodes until they are purged.
func deleteUsers(ctx context.Context, q *db.Queries, ids []int64, now int64) error {
	if err := q.BurnUsersSlots(ctx, db.BurnUsersSlotsParams{BurnedAt: sql.NullInt64{Int64: now, Valid: true}, Ids: ids}); err != nil {
		return err
	}
	if err := q.DeleteUsersBoundDevices(ctx, ids); err != nil {
		return err
	}
	return q.DeleteUsers(ctx, ids)
}

// Bulk actions of the admin's list.
const (
	BulkExtend  = "extend"
	BulkReset   = "reset"
	BulkDisable = "disable"
	BulkEnable  = "enable"
	BulkDelete  = "delete"
	// Where a user is listed, not what the user may do: no word goes to the nodes.
	BulkHide   = "hide"
	BulkUnhide = "unhide"
	BulkMove   = "move"
)

// BulkOpt is what an action needs besides the users.
type BulkOpt struct {
	// Days is for BulkExtend (0: one paid period).
	Days int64
	// Folder is where BulkMove puts the users; nil takes them out of their folders.
	Folder *int64
}

// extension is what BulkExtend adds to each user's term.
func (o BulkOpt) extension() Extension { return Extension{Days: o.Days, Period: o.Days <= 0} }

// checkBulk refuses an action Bulk does not know, and one it knows with a bad option.
func checkBulk(action string, opt BulkOpt) error {
	switch action {
	case BulkExtend, BulkReset, BulkDisable, BulkEnable, BulkDelete, BulkHide, BulkUnhide, BulkMove:
	default:
		return fmt.Errorf("bulk action %q", action)
	}
	if action == BulkExtend && opt.Days > MaxExtendDays {
		return fieldErr("days", "bad_days")
	}
	return nil
}

// bulkIDs are the users a bulk action is asked for, each once, in id order: the order the
// rows are locked in.
func bulkIDs(ids []int64) []int64 {
	want := slices.Clone(ids)
	slices.Sort(want)
	return slices.Compact(want)
}

// Bulk does one action to every user of ids in one transaction: all of them, or, if one
// fails, none (half a list applied, and no record of it, was what a loop of single
// changes left behind). A user that is gone is skipped; a user listed twice is done once.
// It returns how many users changed.
//
// The users are locked in id order and changed by a few set-based statements, so READ
// COMMITTED is enough: what each change reads (the expiry an extension adds to) comes
// from the locked rows, and the traffic batches lock the same rows in the same order.
func (s *Users) Bulk(ctx context.Context, ids []int64, action string, opt BulkOpt) (int, error) {
	if err := checkBulk(action, opt); err != nil {
		return 0, err
	}
	want := bulkIDs(ids)
	done := 0
	err := s.st.TxRC(ctx, func(q *db.Queries) error {
		done = 0
		// The folder first, then the users: the order a deleted folder takes its own.
		if action == BulkMove && opt.Folder != nil {
			if err := lockFolder(ctx, q, *opt.Folder); err != nil {
				return err
			}
		}
		lock := q.LockUserRows
		if action == BulkDelete {
			lock = q.LockUserRowsForDelete
		}
		users, err := lock(ctx, want)
		if err != nil || len(users) == 0 {
			return err
		}
		found := make([]int64, len(users))
		for i, u := range users {
			found[i] = u.ID
		}
		now := s.now()
		switch action {
		case BulkExtend:
			ext := opt.extension()
			p := db.SetUsersExpiryParams{UpdatedAt: now.Unix(), Ids: found, ExpiresAt: make([]int64, len(users))}
			for i, u := range users {
				p.ExpiresAt[i] = ext.until(now, u.ExpiresAt, u.BillingDay).Unix()
			}
			err = q.SetUsersExpiry(ctx, p)
		case BulkReset:
			err = startPeriods(ctx, q, found, now)
		case BulkDisable, BulkEnable:
			status := "active"
			if action == BulkDisable {
				status = "disabled"
			}
			err = q.SetUsersStatus(ctx, db.SetUsersStatusParams{Status: status, UpdatedAt: now.Unix(), Ids: found})
		case BulkDelete:
			err = deleteUsers(ctx, q, found, now.Unix())
		case BulkHide, BulkUnhide:
			err = q.SetUsersHidden(ctx, db.SetUsersHiddenParams{Hidden: Flag(action == BulkHide), UpdatedAt: now.Unix(), Ids: found})
		case BulkMove:
			folder := NullInt(opt.Folder)
			err = q.SetUsersFolder(ctx, db.SetUsersFolderParams{FolderID: folder, UpdatedAt: now.Unix(), Ids: found})
		}
		if err != nil {
			return err
		}
		done = len(found)
		return nil
	})
	if err != nil {
		return 0, err
	}
	if done > 0 && action != BulkHide && action != BulkUnhide && action != BulkMove {
		s.changes.PoliciesChanged()
	}
	return done, nil
}

func encodeTags(tags []string) (string, error) {
	clean := make([]string, 0, len(tags))
	for _, t := range tags {
		if t = strings.TrimSpace(t); t != "" {
			clean = append(clean, t)
		}
	}
	raw, err := json.Marshal(clean)
	return string(raw), err
}

func DecodeTags(raw string) []string {
	var t []string
	_ = json.Unmarshal([]byte(raw), &t)
	if t == nil {
		t = []string{}
	}
	return t
}

func DecodeInbounds(raw sql.NullString) []int64 {
	if !raw.Valid {
		return nil
	}
	var ids []int64
	_ = json.Unmarshal([]byte(raw.String), &ids)
	return ids
}
