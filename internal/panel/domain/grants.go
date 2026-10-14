package domain

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"

	"mikan/internal/panel/store/db"
)

// Traffic packages (GitHub issue #12): a grant is extra traffic a user got, for the main
// quota or one pool, bought or given by the admin. The period's base quota is spent
// first, then the grants, the soonest to expire first. Grants are spent as the counters
// come in, on the counters' transaction, so a node's batch is spent once; what is left
// of them is added to the quota the nodes get.

// Lifetimes of packages and grants.
const (
	LifetimeUsed   = "used"   // until used up
	LifetimePeriod = "period" // until the end of the traffic period it was given in
	LifetimeDays   = "days"   // N days from when it was given
)

// Sources of grants.
const (
	SourcePurchase = "purchase"
	SourceAdmin    = "admin"
)

// Limits of a package or a grant.
const (
	GiB            = int64(1) << 30
	MinGrantBytes  = GiB
	MaxGrantBytes  = 100 << 40
	MaxGrantDays   = 3650
	maxGrantNoteLn = 200
)

// FieldError is input the domain refuses: Field is the input's name, Code says why.
type FieldError struct{ Field, Code string }

func (e *FieldError) Error() string { return e.Field + ": " + e.Code }

func fieldErr(field, code string) error { return &FieldError{Field: field, Code: code} }

// TrafficLeft is what the user may still use of a quota: the base limit's rest plus the
// active grants. -1: unlimited (no limit), which never touches grants.
func TrafficLeft(limit sql.NullInt64, used, grants int64) int64 {
	if !limit.Valid {
		return -1
	}
	return max(0, limit.Int64-used) + max(0, grants)
}

// Overflow is the part of n new bytes past the base quota, used being what the period
// counted before them: that part is taken from the grants.
func Overflow(limit sql.NullInt64, used, n int64) int64 {
	if !limit.Valid || n <= 0 {
		return 0
	}
	return min(n, max(0, used+n-limit.Int64))
}

// GrantsLeft is what is left of the active grants, by user and pool (0: main traffic).
type GrantsLeft map[[2]int64]int64

func (g GrantsLeft) Main(userID int64) int64         { return g[[2]int64{userID, 0}] }
func (g GrantsLeft) Pool(userID, poolID int64) int64 { return g[[2]int64{userID, poolID}] }

// LoadGrantsLeft reads what is left of every user's active grants.
func LoadGrantsLeft(ctx context.Context, q *db.Queries, now time.Time) (GrantsLeft, error) {
	rows, err := q.SumGrantsLeft(ctx, now.Unix())
	if err != nil {
		return nil, err
	}
	out := GrantsLeft{}
	for _, r := range rows {
		out[[2]int64{r.UserID, r.PoolID}] = r.LeftBytes
	}
	return out, nil
}

// UserGrantsLeft reads what is left of one user's active grants.
func UserGrantsLeft(ctx context.Context, q *db.Queries, userID int64, now time.Time) (GrantsLeft, error) {
	rows, err := q.SumUserGrantsLeft(ctx, db.SumUserGrantsLeftParams{UserID: userID, Now: now.Unix()})
	if err != nil {
		return nil, err
	}
	out := GrantsLeft{}
	for _, r := range rows {
		out[[2]int64{r.UserID, r.PoolID}] = r.LeftBytes
	}
	return out, nil
}

// Bytes is an amount of traffic.
type Bytes struct{ Up, Down int64 }

// TrafficBatch is traffic to count: main traffic by user, pool traffic by user and pool.
type TrafficBatch struct {
	Main  map[int64]Bytes
	Pools map[[2]int64]Bytes // {user, pool}
	// Node is the node that carried the batch: what the users' statistics count is counted
	// to it too, so the nodes' traffic adds up to the users'. 0: no node (not counted).
	Node int64
}

// CountTraffic counts a batch on q's transaction in a few set-based statements: the
// users' counters, the pools', the statistics, and what went past a base quota taken
// from the grants. Users and pools deleted meanwhile are skipped, the rest still counts.
//
// READ COMMITTED is enough: the users' rows are locked first, in id order, so batches
// (and period resets, which lock the user first too) take turns per user instead of
// aborting each other; every counter adds in place, and what the grants pay is worked out
// from the counters the locked rows return and from the grants locked after them.
func CountTraffic(ctx context.Context, q *db.Queries, b TrafficBatch, now time.Time) error {
	var poolIDs []int64
	seen := map[int64]bool{}
	for k, t := range b.Pools {
		if t != (Bytes{}) && !seen[k[1]] {
			seen[k[1]] = true
			poolIDs = append(poolIDs, k[1])
		}
	}
	pools := map[int64]bool{}
	if len(poolIDs) > 0 {
		slices.Sort(poolIDs)
		ids, err := q.LockTrafficPools(ctx, poolIDs)
		if err != nil {
			return err
		}
		for _, id := range ids {
			pools[id] = true
		}
	}
	// What each user's row takes: main traffic and the traffic of pools that still exist.
	type sums struct{ main, pool Bytes }
	byUser := map[int64]*sums{}
	of := func(id int64) *sums {
		if byUser[id] == nil {
			byUser[id] = &sums{}
		}
		return byUser[id]
	}
	for id, t := range b.Main {
		if t != (Bytes{}) {
			s := of(id)
			s.main.Up, s.main.Down = s.main.Up+t.Up, s.main.Down+t.Down
		}
	}
	for k, t := range b.Pools {
		if t != (Bytes{}) && pools[k[1]] {
			s := of(k[0])
			s.pool.Up, s.pool.Down = s.pool.Up+t.Up, s.pool.Down+t.Down
		}
	}
	if len(byUser) == 0 {
		return nil
	}
	want := make([]int64, 0, len(byUser))
	for id := range byUser {
		want = append(want, id)
	}
	slices.Sort(want)
	users, err := q.LockUsers(ctx, want)
	if err != nil || len(users) == 0 {
		return err
	}
	var p db.AddUsersTrafficParams
	var stats db.AddTrafficHourlyBatchParams
	for _, id := range users {
		s := byUser[id]
		p.Ids, p.Up, p.Down = append(p.Ids, id), append(p.Up, s.main.Up), append(p.Down, s.main.Down)
		p.PoolUp, p.PoolDown = append(p.PoolUp, s.pool.Up), append(p.PoolDown, s.pool.Down)
		stats.UserIds = append(stats.UserIds, id)
		stats.Up, stats.Down = append(stats.Up, s.main.Up+s.pool.Up), append(stats.Down, s.main.Down+s.pool.Down)
	}
	counted, err := q.AddUsersTraffic(ctx, p)
	if err != nil {
		return err
	}
	// What went past a base quota, by user and target (pool 0: the main traffic).
	over := map[[2]int64]int64{}
	for _, r := range counted {
		m := byUser[r.ID].main
		if n := Overflow(r.TrafficLimit, r.Used-m.Up-m.Down, m.Up+m.Down); n > 0 {
			over[[2]int64{r.ID, 0}] = n
		}
	}
	locked := make(map[int64]bool, len(users))
	for _, id := range users {
		locked[id] = true
	}
	var pp db.AddUserPoolsTrafficParams
	keys := make([][2]int64, 0, len(b.Pools))
	for k, t := range b.Pools {
		if t != (Bytes{}) && pools[k[1]] && locked[k[0]] {
			keys = append(keys, k)
		}
	}
	slices.SortFunc(keys, func(a, b [2]int64) int { return cmp.Or(cmp.Compare(a[0], b[0]), cmp.Compare(a[1], b[1])) })
	for _, k := range keys {
		t := b.Pools[k]
		pp.UserIds, pp.PoolIds, pp.Up, pp.Down = append(pp.UserIds, k[0]), append(pp.PoolIds, k[1]), append(pp.Up, t.Up), append(pp.Down, t.Down)
	}
	if len(keys) > 0 {
		rows, err := q.AddUserPoolsTraffic(ctx, pp)
		if err != nil {
			return err
		}
		for _, r := range rows {
			t := b.Pools[[2]int64{r.UserID, r.PoolID}]
			if n := Overflow(r.TrafficLimit, r.Used-t.Up-t.Down, t.Up+t.Down); n > 0 {
				over[[2]int64{r.UserID, r.PoolID}] = n
			}
		}
	}
	if err := spendGrants(ctx, q, over, now); err != nil {
		return err
	}
	if err := q.AddTrafficHourlyBatch(ctx, db.AddTrafficHourlyBatchParams{UserIds: stats.UserIds, Hour: now.Unix() / 3600, Up: stats.Up, Down: stats.Down}); err != nil {
		return err
	}
	if err := q.AddTrafficDailyBatch(ctx, db.AddTrafficDailyBatchParams{UserIds: stats.UserIds, Day: now.Unix() / 86400, Up: stats.Up, Down: stats.Down}); err != nil {
		return err
	}
	if b.Node == 0 {
		return nil
	}
	// The same bytes the users' statistics just took (users and pools deleted meanwhile
	// are not in them), so the sum over nodes equals the sum over users.
	var up, down int64
	for i := range stats.UserIds {
		up, down = up+stats.Up[i], down+stats.Down[i]
	}
	if up == 0 && down == 0 {
		return nil
	}
	// By whom: one row per user and node with what they carried over it, on the same
	// transaction and from the same survivors, so a node's clients add up to what it carried.
	if err := q.AddNodeClientsTraffic(ctx, db.AddNodeClientsTrafficParams{
		NodeID: b.Node, Now: now.Unix(), UserIds: stats.UserIds, Up: stats.Up, Down: stats.Down,
	}); err != nil {
		return err
	}
	return q.AddNodeTraffic(ctx, db.AddNodeTrafficParams{NodeID: b.Node, Hour: now.Unix() / 3600, Day: now.Unix() / 86400, Up: up, Down: down})
}

// spendGrants takes what went past the base quotas from the active grants of each target
// in the spending order; only the users who went past one are read. What no grant covers
// is not owed later: the node let it through before it learned the quota was out.
func spendGrants(ctx context.Context, q *db.Queries, over map[[2]int64]int64, now time.Time) error {
	if len(over) == 0 {
		return nil
	}
	seen := map[int64]bool{}
	var users []int64
	for k := range over {
		if !seen[k[0]] {
			seen[k[0]] = true
			users = append(users, k[0])
		}
	}
	slices.Sort(users)
	gs, err := q.LockSpendableGrants(ctx, db.LockSpendableGrantsParams{UserIds: users, Now: now.Unix()})
	if err != nil {
		return err
	}
	var spend db.SpendGrantsParams
	for _, g := range gs {
		k := [2]int64{g.UserID, g.PoolID}
		if take := min(over[k], g.Remaining); take > 0 {
			over[k] -= take
			spend.Ids, spend.Spent = append(spend.Ids, g.ID), append(spend.Spent, take)
		}
	}
	if len(spend.Ids) == 0 {
		return nil
	}
	return q.SpendGrants(ctx, spend)
}

// StartPeriod begins a new traffic period on q's transaction: the main and pool counters
// of the period drop to zero and the grants that last one period end. Other grants keep
// what is left of them.
func StartPeriod(ctx context.Context, q *db.Queries, userID, start int64, now time.Time) error {
	if err := q.ResetUserTraffic(ctx, db.ResetUserTrafficParams{PeriodStart: start, UpdatedAt: now.Unix(), ID: userID}); err != nil {
		return err
	}
	return endPeriod(ctx, q, userID, now)
}

// StartPeriodIfOlder is StartPeriod for the scheduled resets, decided on a row read
// before: it begins the period only while the user's current one started before start,
// so a period a payment began meanwhile is not reset again. It says whether it did.
func StartPeriodIfOlder(ctx context.Context, q *db.Queries, userID, start int64, now time.Time) (bool, error) {
	n, err := q.StartPeriodIfOlder(ctx, db.StartPeriodIfOlderParams{PeriodStart: start, UpdatedAt: now.Unix(), ID: userID})
	if err != nil || n == 0 {
		return false, err
	}
	return true, endPeriod(ctx, q, userID, now)
}

// startPeriods is StartPeriod now for many users at once.
func startPeriods(ctx context.Context, q *db.Queries, ids []int64, now time.Time) error {
	if err := q.ResetUsersTraffic(ctx, db.ResetUsersTrafficParams{Now: now.Unix(), Ids: ids}); err != nil {
		return err
	}
	if err := q.ResetUsersPools(ctx, ids); err != nil {
		return err
	}
	return q.EndUsersPeriodGrants(ctx, db.EndUsersPeriodGrantsParams{Now: now.Unix(), Ids: ids})
}

func endPeriod(ctx context.Context, q *db.Queries, userID int64, now time.Time) error {
	if err := q.ResetUserPools(ctx, userID); err != nil {
		return err
	}
	return q.EndPeriodGrants(ctx, db.EndPeriodGrantsParams{Now: now.Unix(), UserID: userID})
}

// GrantSpec is traffic to give a user.
type GrantSpec struct {
	PoolID    int64 // 0: the main traffic
	Bytes     int64
	Lifetime  string
	Days      int64 // LifetimeDays
	Source    string
	PaymentID int64 // 0: none
	PackageID int64 // 0: none
	Note      string
}

// GrantOf is what a package gives when it is bought with payment paymentID.
func GrantOf(p db.TrafficPackage, paymentID int64) GrantSpec {
	return GrantSpec{PoolID: p.PoolID.Int64, Bytes: p.Bytes, Lifetime: p.Lifetime, Days: p.Days, Source: SourcePurchase, PaymentID: paymentID, PackageID: p.ID}
}

// checkGrant validates the size and lifetime of a grant or a package.
func checkGrant(bytes int64, lifetime string, days int64) error {
	switch {
	case bytes < MinGrantBytes || bytes > MaxGrantBytes:
		return fieldErr("bytes", "bad_bytes")
	case lifetime != LifetimeUsed && lifetime != LifetimePeriod && lifetime != LifetimeDays:
		return fieldErr("lifetime", "bad_lifetime")
	case lifetime == LifetimeDays && (days < 1 || days > MaxGrantDays):
		return fieldErr("days", "bad_days")
	}
	return nil
}

// GrantTx gives u traffic on q's transaction. A `period` grant ends with u's traffic
// period (also earlier, when a reset starts a new one); without resets it lasts until a
// reset or until used up.
func GrantTx(ctx context.Context, q *db.Queries, u db.User, g GrantSpec, now time.Time) (db.TrafficGrant, error) {
	var expires sql.NullInt64
	switch g.Lifetime {
	case LifetimeDays:
		expires = sql.NullInt64{Int64: now.Unix() + g.Days*day, Valid: true}
	case LifetimePeriod:
		if t, ok := NextReset(u, now); ok {
			expires = sql.NullInt64{Int64: t.Unix(), Valid: true}
		}
	}
	return q.CreateTrafficGrant(ctx, db.CreateTrafficGrantParams{
		UserID: u.ID, PoolID: sql.NullInt64{Int64: g.PoolID, Valid: g.PoolID != 0}, Bytes: g.Bytes, Remaining: g.Bytes,
		Lifetime: g.Lifetime, ExpiresAt: expires, Source: g.Source,
		PaymentID: sql.NullInt64{Int64: g.PaymentID, Valid: g.PaymentID != 0},
		PackageID: sql.NullInt64{Int64: g.PackageID, Valid: g.PackageID != 0},
		Note:      g.Note, CreatedAt: now.Unix(),
	})
}

// GrantInput is the admin giving a user traffic.
type GrantInput struct {
	PoolID   int64 // 0: the main traffic
	Bytes    int64
	Lifetime string
	Days     int64
	Note     string
}

// Grant gives the user traffic from the admin. A user (or a pool) that ran out gets back
// in at once: the nodes get the bigger quota.
func (s *Users) Grant(ctx context.Context, userID int64, in GrantInput) (db.TrafficGrant, error) {
	in.Note = strings.TrimSpace(in.Note)
	if err := checkGrant(in.Bytes, in.Lifetime, in.Days); err != nil {
		return db.TrafficGrant{}, err
	}
	if len([]rune(in.Note)) > maxGrantNoteLn {
		return db.TrafficGrant{}, fieldErr("note", "note_too_long")
	}
	var g db.TrafficGrant
	err := s.st.Tx(ctx, func(q *db.Queries) error {
		u, err := q.GetUser(ctx, userID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := poolExists(ctx, q, in.PoolID); err != nil {
			return err
		}
		g, err = GrantTx(ctx, q, u, GrantSpec{PoolID: in.PoolID, Bytes: in.Bytes, Lifetime: in.Lifetime, Days: in.Days, Source: SourceAdmin, Note: in.Note}, s.now())
		return err
	})
	if err != nil {
		return db.TrafficGrant{}, err
	}
	s.changes.PoliciesChanged()
	return g, nil
}

// poolExists: id 0 (the main traffic) or a pool that exists.
func poolExists(ctx context.Context, q *db.Queries, id int64) error {
	if id == 0 {
		return nil
	}
	_, err := q.GetTrafficPool(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return fieldErr("pool_id", "pool_not_found")
	}
	return err
}

// GrantActive: the grant still counts at now.
func GrantActive(g db.TrafficGrant, now time.Time) bool {
	return g.Remaining > 0 && (!g.ExpiresAt.Valid || g.ExpiresAt.Int64 > now.Unix())
}
