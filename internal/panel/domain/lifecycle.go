package domain

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

// The lifecycle of a subscription past its plain term (roadmap #10): the traffic a period
// leaves (carry-over), a term that waits for the first connection (starts.go), the tariff
// that follows the current one (next.go) and the removal of subscriptions nobody uses any
// more (inactive.go).

// KeyLifecycle is the setting Lifecycle is kept under; unset: DefaultLifecycle.
const KeyLifecycle = "user_lifecycle"

// Lifecycle is what the admin decides for every subscription; a tariff may decide the
// carry-over for its own (TariffLifecycle).
type Lifecycle struct {
	Carry   CarryRules   `json:"carry"`
	Removal RemovalRules `json:"removal"`
}

// CarryRules: when a renewal or a switch to the next tariff starts a new traffic period, the
// main traffic the old one left goes into the new one (Carried). Off by default: until now
// a period took nothing over, and a panel keeps doing what it did until its admin says.
type CarryRules struct {
	On bool `json:"on" doc:"Переносить остаток трафика периода в новый период при продлении и при переходе на следующий тариф. Тариф может решить по-своему"`
	// Cap bounds what carries, in percent of the new period's quota: at 100 a subscriber who
	// used nothing starts the new period with two quotas, never more.
	Cap int `json:"cap_percent" minimum:"1" maximum:"100" doc:"Сколько можно перенести: процент квоты нового периода"`
}

// RemovalRules: the panel removes a subscription Days after its term ended and after its last
// connection, both, unless something of its subscriber's is still in it (InactiveUser.Keep).
// Off by default: a removal cannot be undone.
type RemovalRules struct {
	On   bool `json:"on" doc:"Удалять неактивные подписки сами"`
	Days int  `json:"days" minimum:"7" maximum:"3650" doc:"Через сколько дней после конца срока и последнего подключения"`
}

// DefaultLifecycle: nothing carries and nothing is removed; turned on, the whole quota may
// carry and a subscription goes a month after its end.
var DefaultLifecycle = Lifecycle{Carry: CarryRules{Cap: 100}, Removal: RemovalRules{Days: 30}}

// The bounds of the rules. A week at least before a removal: a subscriber who pays a few
// days late finds the subscription and its link where they were.
const (
	MinRemovalDays = 7
	MaxRemovalDays = 3650
)

// Validate checks the rules the admin sends; the error names the field under its part.
func (l Lifecycle) Validate() error {
	switch {
	case l.Carry.Cap < 1 || l.Carry.Cap > 100:
		return fieldErr("carry.cap_percent", "bad_carry_cap")
	case l.Removal.Days < MinRemovalDays || l.Removal.Days > MaxRemovalDays:
		return fieldErr("removal.days", "bad_removal_days")
	}
	return nil
}

// LoadLifecycle reads the admin's rules; DefaultLifecycle until they set any. Rules stored
// broken fall back to the default, which carries and removes nothing: only a database that
// cannot be read is an error.
func LoadLifecycle(ctx context.Context, set *settings.Settings) (Lifecycle, error) {
	raw, found, err := set.Raw(ctx, KeyLifecycle)
	if err != nil || !found {
		return DefaultLifecycle, err
	}
	l := DefaultLifecycle
	if json.Unmarshal([]byte(raw), &l) != nil || l.Validate() != nil {
		return DefaultLifecycle, nil
	}
	return l, nil
}

// TariffLifecycle is a tariff's own part: Carry over the panel's rule (invalid: as the panel
// has it), and whether a subscription made on it waits for its first connection to start
// its term.
type TariffLifecycle struct {
	Carry          sql.NullBool
	StartOnConnect bool
}

// LoadTariffLifecycle reads tariff id's own part; a tariff without one has the panel's.
func LoadTariffLifecycle(ctx context.Context, q *db.Queries, id int64) (TariffLifecycle, error) {
	r, err := q.GetTariffLifecycle(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return TariffLifecycle{}, nil
	}
	return TariffLifecycle{Carry: r.CarryOver, StartOnConnect: r.StartOnConnect}, err
}

// TariffLifecycles reads every tariff's own part, by tariff id: one read for a list.
func TariffLifecycles(ctx context.Context, q *db.Queries) (map[int64]TariffLifecycle, error) {
	rows, err := q.ListTariffLifecycles(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]TariffLifecycle, len(rows))
	for _, r := range rows {
		out[r.TariffID] = TariffLifecycle{Carry: r.CarryOver, StartOnConnect: r.StartOnConnect}
	}
	return out, nil
}

// SetTariffLifecycle writes tariff id's own part on q's transaction.
func SetTariffLifecycle(ctx context.Context, q *db.Queries, id int64, tl TariffLifecycle) error {
	return q.SetTariffLifecycle(ctx, db.SetTariffLifecycleParams{TariffID: id, CarryOver: tl.Carry, StartOnConnect: tl.StartOnConnect})
}

// CarriesOver says whether a new period on a tariff with its own part tl takes over what the
// old one left, by rules: the tariff's word first.
func (rules CarryRules) CarriesOver(tl TariffLifecycle) bool {
	if tl.Carry.Valid {
		return tl.Carry.Bool
	}
	return rules.On
}

// minCarry: less than a mebibyte left of a quota is not carried, where it would be a package
// of its own in the subscriber's list for next to nothing.
const minCarry = 1 << 20

// Carried is what of the main traffic a period leaves goes into the next one: the base quota
// left, limit less used. Never the traffic packages, which keep what is left of them by their
// own lifetimes, nor a server group's own limit. At most capPercent of the new period's
// quota next. A period without a limit leaves nothing to count, and a new one without a
// limit needs nothing.
func Carried(limit sql.NullInt64, used int64, next sql.NullInt64, capPercent int) int64 {
	if !limit.Valid || !next.Valid || capPercent <= 0 {
		return 0
	}
	left := limit.Int64 - used
	// The quota first divided, so that a huge one does not overflow on the way.
	bound := next.Int64/100*int64(capPercent) + next.Int64%100*int64(capPercent)/100
	carried := min(left, bound)
	if carried < minCarry {
		return 0
	}
	return carried
}

// carryInto is what u's period leaves that a new period of u's on tariff t takes over, by the
// panel's rules and t's own (Carried), read on q's transaction.
func carryInto(ctx context.Context, q *db.Queries, u db.User, t db.Tariff) (int64, error) {
	rules, err := LoadLifecycle(ctx, settings.New(q))
	if err != nil {
		return 0, err
	}
	tl, err := LoadTariffLifecycle(ctx, q, t.ID)
	if err != nil || !rules.Carry.CarriesOver(tl) {
		return 0, err
	}
	return Carried(u.TrafficLimit, u.UsedUp+u.UsedDown, t.TrafficLimit, rules.Carry.Cap), nil
}

// grantCarry gives u, whose new period has just begun, what the old one left, as a package
// spent after the base quota until the new period ends. Being a package, it is not carried
// again by the next renewal, and a refund of the payment that started the period (paymentID,
// 0: none) takes it back with the payment's other packages.
func grantCarry(ctx context.Context, q *db.Queries, u db.User, bytes, paymentID int64, now time.Time) error {
	if bytes <= 0 {
		return nil
	}
	_, err := GrantTx(ctx, q, u, GrantSpec{Bytes: bytes, Lifetime: LifetimePeriod, Source: SourceCarry, PaymentID: paymentID}, now)
	return err
}
