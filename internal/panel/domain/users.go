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
	"strings"
	"time"

	"mikan/internal/panel/secure"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

const (
	StateActive   = "active"
	StateExpiring = "expiring"
	StateLimited  = "limited"
	StateExpired  = "expired"
	StateDisabled = "disabled"

	expiringWindow = 7 * 24 * time.Hour
	day            = int64(24 * time.Hour / time.Second)
)

var (
	ErrNotFound = errors.New("not found")
	ErrNoSlots  = errors.New("no free slots")
)

// State derives what the user can do right now; limited and expired are never stored.
// grants is what is left of the user's active main grants (GrantsLeft.Main): a user past
// the base quota with grants left is not limited.
func State(u db.User, grants int64, now time.Time) string {
	switch {
	case u.Status == "disabled":
		return StateDisabled
	case u.ExpiresAt.Valid && now.Unix() >= u.ExpiresAt.Int64:
		return StateExpired
	case TrafficLeft(u.TrafficLimit, u.UsedUp+u.UsedDown, grants) == 0:
		return StateLimited
	case u.ExpiresAt.Valid && time.Unix(u.ExpiresAt.Int64, 0).Sub(now) <= expiringWindow:
		return StateExpiring
	}
	return StateActive
}

func CanConnect(state string) bool { return state == StateActive || state == StateExpiring }

// CountStates counts the users in each State in the database, without loading them.
func CountStates(ctx context.Context, q *db.Queries, now time.Time) (db.CountUserStatesRow, error) {
	return q.CountUserStates(ctx, db.CountUserStatesParams{Now: now.Unix(), ExpiringWithin: int64(expiringWindow / time.Second)})
}

// NextReset is when the traffic counter of the current period drops to zero.
func NextReset(u db.User, now time.Time) (time.Time, bool) {
	switch u.ResetStrategy {
	case "month_start":
		// Monthly: on the billing day, or the 1st without one.
		return nextMonthPeriod(MonthPeriodStart(now, u.BillingDay), u.BillingDay), true
	case "period":
		return time.Unix(u.PeriodStart+max(u.PeriodDays, 1)*day, 0).UTC(), true
	}
	return time.Time{}, false
}

// Changes tells the node syncer what to push after a mutation.
type Changes interface {
	PoliciesChanged()
	SlotsChanged()
}

type Users struct {
	st      *store.Store
	now     func() time.Time
	pool    *Pool
	changes Changes
}

func NewUsers(st *store.Store, pool *Pool, changes Changes, now func() time.Time) *Users {
	return &Users{st: st, now: now, pool: pool, changes: changes}
}

// Where a user came from, kept for the admin's list (users.source). It is a record, not a
// permission: nothing reads it to decide what a user may do.
const (
	UserFromAdmin  = "admin"  // made in the panel or by an API key
	UserFromBot    = "bot"    // bought in the Telegram bot
	UserFromTrial  = "trial"  // the bot's free trial
	UserFromImport = "import" // brought over from another panel
)

// UserOrigins lists them in the order the list shows them.
var UserOrigins = []string{UserFromAdmin, UserFromBot, UserFromTrial, UserFromImport}

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
}

// Create makes a user the admin names (the panel, an API key): the name is checked as
// CleanUserName does.
func (s *Users) Create(ctx context.Context, in CreateInput) (db.User, error) {
	name, err := CleanUserName(in.Name)
	if err != nil {
		return db.User{}, err
	}
	in.Name = name
	u, err := s.create(ctx, in)
	if errors.Is(err, ErrNoSlots) {
		if err := s.pool.Refill(ctx, RefillBatch); err != nil {
			return db.User{}, err
		}
		s.changes.SlotsChanged()
		u, err = s.create(ctx, in)
	}
	if err != nil {
		return db.User{}, err
	}
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
	return u, ApplyTariffPools(ctx, q, u.ID, t.ID)
}

// Purchase applies a paid tariff on q's transaction, so the payment and its effect commit
// together. userID 0 (or a user deleted since the invoice) makes a new subscription named
// name. A renewal takes the tariff's limits, adds its term after the current one (from
// now when that already ended) and turns the user on; resetTraffic also starts a new
// traffic period (the payment buys a full quota). term is the term bought, as it was
// when bought (invalid: the tariff's own). ErrNoSlots: refill and run again.
// The caller calls Changed after the commit.
func (s *Users) Purchase(ctx context.Context, q *db.Queries, userID, tariffID int64, term sql.NullInt64, name string, resetTraffic bool) (u db.User, created bool, err error) {
	if userID != 0 {
		u, err = q.GetUser(ctx, userID)
	}
	if userID == 0 || errors.Is(err, sql.ErrNoRows) {
		u, err = s.createTx(ctx, q, CreateInput{Name: name, Note: "Telegram", TariffID: tariffID, TermDays: term, Source: UserFromBot}, true)
		return u, err == nil, err
	}
	if err != nil {
		return u, false, err
	}
	t, err := q.GetTariff(ctx, tariffID)
	if err != nil {
		return u, false, err
	}
	now := s.now()
	base := now
	if u.ExpiresAt.Valid && time.Unix(u.ExpiresAt.Int64, 0).After(now) {
		base = time.Unix(u.ExpiresAt.Int64, 0)
	}
	billingDay := t.BillingDay
	if !billingDay.Valid {
		billingDay = u.BillingDay
	}
	u, err = q.UpdateUser(ctx, db.UpdateUserParams{
		Name: u.Name, Contact: u.Contact, Note: u.Note, Tags: u.Tags, Status: "active", TariffID: sql.NullInt64{Int64: t.ID, Valid: true},
		TrafficLimit: t.TrafficLimit, DeviceLimit: t.DeviceLimit, SpeedLimit: t.SpeedLimit, ResetStrategy: t.ResetStrategy, PeriodDays: u.PeriodDays, PeriodStart: u.PeriodStart,
		ExpiresAt: tariffExpiry(base, durationTariff{termDays(t, term), billingDay}), Inbounds: u.Inbounds, BillingDay: billingDay,
		UpdatedAt: now.Unix(), ID: u.ID,
	})
	if err != nil {
		return u, false, err
	}
	if err := ApplyTariffPools(ctx, q, u.ID, t.ID); err != nil {
		return u, false, err
	}
	if !resetTraffic {
		return u, false, nil
	}
	if err := StartPeriod(ctx, q, u.ID, now.Unix(), now); err != nil {
		return u, false, err
	}
	u, err = q.GetUser(ctx, u.ID)
	return u, false, err
}

// RefillSlots tops the slot pool up after ErrNoSlots.
func (s *Users) RefillSlots(ctx context.Context) error {
	if err := s.pool.Refill(ctx, RefillBatch); err != nil {
		return err
	}
	s.changes.SlotsChanged()
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
	return sql.NullInt64{Int64: from + days*day, Valid: true}
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
	// FolderID puts the user into a folder; ClearFolder takes the user out of theirs.
	FolderID    *int64
	ClearFolder bool
	// Extend adds a term to the expiry the transaction reads, so a payment that lands
	// meanwhile is not overwritten by an absolute date worked out before it.
	Extend *Extension
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
	if exp.Valid && time.Unix(exp.Int64, 0).After(now) {
		base = time.Unix(exp.Int64, 0)
	}
	switch {
	case e.Period && billingDay.Valid:
		return AddMonths(base, 1, billingDay)
	case e.Period:
		return base.Add(30 * 24 * time.Hour)
	case e.Months > 0:
		return AddMonths(base, e.Months, billingDay)
	}
	return base.Add(time.Duration(e.Days) * 24 * time.Hour)
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
	par := db.UpdateUserParams{
		Name: u.Name, Contact: u.Contact, Note: u.Note, Tags: u.Tags, Status: u.Status, TariffID: u.TariffID,
		TrafficLimit: u.TrafficLimit, DeviceLimit: u.DeviceLimit, SpeedLimit: u.SpeedLimit, ResetStrategy: u.ResetStrategy,
		PeriodDays: u.PeriodDays, PeriodStart: u.PeriodStart, ExpiresAt: u.ExpiresAt, Inbounds: u.Inbounds,
		BillingDay: u.BillingDay, UpdatedAt: now, ID: u.ID,
	}
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
		par.SpeedLimit = t.SpeedLimit
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
		par.BillingDay = sql.NullInt64{Int64: *p.BillingDay, Valid: true}
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
		par.TrafficLimit = sql.NullInt64{Int64: *p.TrafficLimit, Valid: true}
	}
	switch {
	case p.ClearDeviceLimit:
		par.DeviceLimit = sql.NullInt64{}
	case p.DeviceLimit != nil:
		par.DeviceLimit = sql.NullInt64{Int64: *p.DeviceLimit, Valid: true}
	}
	switch {
	case p.ClearSpeedLimit:
		par.SpeedLimit = sql.NullInt64{}
	case p.SpeedLimit != nil:
		if !ValidSpeedLimit(*p.SpeedLimit) {
			return db.User{}, ErrBadSpeedLimit
		}
		par.SpeedLimit = sql.NullInt64{Int64: *p.SpeedLimit, Valid: true}
	}
	switch {
	case p.ClearExpiry:
		par.ExpiresAt = sql.NullInt64{}
	case p.ExpiresAt != nil:
		par.ExpiresAt = sql.NullInt64{Int64: p.ExpiresAt.Unix(), Valid: true}
	}
	if p.Extend != nil {
		par.ExpiresAt = sql.NullInt64{Int64: p.Extend.until(s.now(), par.ExpiresAt, par.BillingDay).Unix(), Valid: true}
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
	// Where the user is listed is not one of the fields above: it changes on its own
	// statements, and the row returned follows.
	if p.Hidden != nil {
		out.Hidden = b2i(*p.Hidden)
		if err := q.SetUserHidden(ctx, db.SetUserHiddenParams{Hidden: out.Hidden, UpdatedAt: now, ID: id}); err != nil {
			return out, err
		}
	}
	if p.FolderID != nil || p.ClearFolder {
		out.FolderID = sql.NullInt64{}
		if p.FolderID != nil && !p.ClearFolder {
			out.FolderID = sql.NullInt64{Int64: *p.FolderID, Valid: true}
		}
		if err := q.SetUserFolder(ctx, db.SetUserFolderParams{FolderID: out.FolderID, UpdatedAt: now, ID: id}); err != nil {
			return out, err
		}
	}
	return out, nil
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
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

// ExtendMonths adds n months: to the n-th billing day, or without one to the same day of
// the month (see AddMonths). A term that already ended restarts from now.
func (s *Users) ExtendMonths(ctx context.Context, id int64, n int) (db.User, error) {
	return s.Update(ctx, id, Patch{Extend: &Extension{Months: n}})
}

// ExtendPeriod adds one paid period: a month up to the billing day, or 30 days without one.
func (s *Users) ExtendPeriod(ctx context.Context, id int64) (db.User, error) {
	return s.Update(ctx, id, Patch{Extend: &Extension{Period: true}})
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
	err := s.reissue(ctx, id)
	if errors.Is(err, ErrNoSlots) {
		if err := s.pool.Refill(ctx, RefillBatch); err != nil {
			return db.User{}, err
		}
		s.changes.SlotsChanged()
		err = s.reissue(ctx, id)
	}
	if err != nil {
		return db.User{}, err
	}
	s.changes.PoliciesChanged()
	return s.Get(ctx, id)
}

func (s *Users) reissue(ctx context.Context, id int64) error {
	now := s.now().Unix()
	return s.st.Tx(ctx, func(q *db.Queries) error {
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
	})
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

// Bulk does one action to every user of ids in one transaction: all of them, or, if one
// fails, none (half a list applied, and no record of it, was what a loop of single
// changes left behind). A user that is gone is skipped; a user listed twice is done once.
// It returns how many users changed.
//
// The users are locked in id order and changed by a few set-based statements, so READ
// COMMITTED is enough: what each change reads (the expiry an extension adds to) comes
// from the locked rows, and the traffic batches lock the same rows in the same order.
func (s *Users) Bulk(ctx context.Context, ids []int64, action string, opt BulkOpt) (int, error) {
	switch action {
	case BulkExtend, BulkReset, BulkDisable, BulkEnable, BulkDelete, BulkHide, BulkUnhide, BulkMove:
	default:
		return 0, fmt.Errorf("bulk action %q", action)
	}
	days := opt.Days
	want := slices.Clone(ids)
	slices.Sort(want)
	want = slices.Compact(want)
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
			ext := Extension{Days: days, Period: days <= 0}
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
			err = q.SetUsersHidden(ctx, db.SetUsersHiddenParams{Hidden: b2i(action == BulkHide), UpdatedAt: now.Unix(), Ids: found})
		case BulkMove:
			folder := sql.NullInt64{}
			if opt.Folder != nil {
				folder = sql.NullInt64{Int64: *opt.Folder, Valid: true}
			}
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
