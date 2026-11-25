package nodesync

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"testing"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
)

// A traffic pool end to end on the panel's side: the user gets the tariff's pool limit,
// the node is told which inbound counts where and what is left, pool bytes go to the
// pool and the all-time totals but not to the main quota, and a main reset resets pools.
func TestTrafficPoolsOnThePanel(t *testing.T) {
	s, node, st, users, _ := setup(t)
	ctx := context.Background()
	q := st.Q
	pool, err := q.CreateTrafficPool(ctx, db.CreateTrafficPoolParams{Name: "WL", CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	ins, _ := q.ListInbounds(ctx)
	wl := ins[0]
	if err := q.SetInboundPool(ctx, db.SetInboundPoolParams{PoolID: sql.NullInt64{Int64: pool.ID, Valid: true}, ID: wl.ID}); err != nil {
		t.Fatal(err)
	}
	tariffs, _ := q.ListTariffs(ctx)
	const limit = 1000
	if err := q.AddTariffPool(ctx, db.AddTariffPoolParams{TariffID: tariffs[1].ID, PoolID: pool.ID, Mode: domain.PoolOwn, TrafficLimit: sql.NullInt64{Int64: limit, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	u, err := users.Create(ctx, domain.CreateInput{Name: "a", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	ups, _ := q.ListUserPools(ctx, u.ID)
	if len(ups) != 1 || !ups[0].TrafficLimit.Valid || ups[0].TrafficLimit.Int64 != limit {
		t.Fatalf("the tariff's pool limit: %+v", ups)
	}

	key := strconv.FormatInt(pool.ID, 10)
	d, _ := s.desired(ctx)
	for _, in := range d.Inbounds {
		if (in.Name == wl.Name) != (in.Pool == key) {
			t.Fatalf("inbound %s pool %q", in.Name, in.Pool)
		}
	}
	slot, _ := q.GetSlot(ctx, u.SlotID.Int64)
	if p := policyOf(t, s, u.ID); len(p.Pools) != 1 || p.Pools[0] != (nodeapi.PoolQuota{Pool: key, Remaining: limit}) {
		t.Fatalf("pool quota: %+v", p.Pools)
	}

	node.batch = nodeapi.Counters{Epoch: "e1", Seq: 1, Slots: map[string]nodeapi.Traffic{slot.Name: {Down: 70}},
		Pools: map[string]map[string]nodeapi.Traffic{slot.Name: {key: {Up: 100, Down: 900}, "999": {Down: 5}}}}
	s.pullCounters(ctx)
	s.pullCounters(ctx) // the same batch again: counted once
	got, _ := q.GetUser(ctx, u.ID)
	ups, _ = q.ListUserPools(ctx, u.ID)
	if got.UsedDown != 70 || got.UsedUp != 0 || got.TotalDown != 970 || got.TotalUp != 100 || ups[0].UsedUp != 100 || ups[0].UsedDown != 900 {
		t.Fatalf("main %d/%d total %d/%d pool %d/%d", got.UsedUp, got.UsedDown, got.TotalUp, got.TotalDown, ups[0].UsedUp, ups[0].UsedDown)
	}
	// The pool is used up: the node gets 0 left, the user may still get in elsewhere.
	if p := policyOf(t, s, u.ID); p.Pools[0].Remaining != 0 || !p.Allowed {
		t.Fatalf("after the pool ran out: %+v", p)
	}
	// The main quota running out keeps the user in for the pool.
	_, err = st.DB.ExecContext(ctx, "UPDATE users SET traffic_limit = 50 WHERE id = $1", u.ID)
	if err != nil {
		t.Fatal(err)
	}
	s.m.PoliciesChanged() // a change outside the API is picked up by the next upkeep

	if p := policyOf(t, s, u.ID); !p.Allowed || p.QuotaRemaining != 0 {
		t.Fatalf("main quota out: %+v", p)
	}
	// A reset of the main traffic resets the pools.
	if _, err := users.ResetTraffic(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	ups, _ = q.ListUserPools(ctx, u.ID)
	if ups[0].UsedUp+ups[0].UsedDown != 0 {
		t.Fatalf("pool after the reset: %+v", ups[0])
	}
}

// A group the user's tariff has in the main traffic goes to the node as counting there
// (Main, with what is left of the main traffic for nodes that do not know Main), and its
// traffic spends the main quota even when a node reports it under the group.
func TestGroupInTheMainTraffic(t *testing.T) {
	s, node, st, users, _ := setup(t)
	ctx := context.Background()
	q := st.Q
	pool, err := q.CreateTrafficPool(ctx, db.CreateTrafficPoolParams{Name: "EU", CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	ins, _ := q.ListInbounds(ctx)
	if err := q.SetInboundPool(ctx, db.SetInboundPoolParams{PoolID: sql.NullInt64{Int64: pool.ID, Valid: true}, ID: ins[0].ID}); err != nil {
		t.Fatal(err)
	}
	tariffs, _ := q.ListTariffs(ctx)
	u, err := users.Create(ctx, domain.CreateInput{Name: "a", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	limit := int64(1000)
	if u, err = users.Update(ctx, u.ID, domain.Patch{TrafficLimit: &limit}); err != nil {
		t.Fatal(err)
	}
	slot, _ := q.GetSlot(ctx, u.SlotID.Int64)
	key := strconv.FormatInt(pool.ID, 10)
	p := policyOf(t, s, u.ID)
	if len(p.Pools) != 1 || p.Pools[0] != (nodeapi.PoolQuota{Pool: key, Remaining: 1000, Main: true}) {
		t.Fatalf("the group in the main traffic: %+v", p.Pools)
	}

	node.batch = nodeapi.Counters{Epoch: "e1", Seq: 1, Pools: map[string]map[string]nodeapi.Traffic{slot.Name: {key: {Down: 300}}}}
	s.pullCounters(ctx)
	got, _ := q.GetUser(ctx, u.ID)
	if got.UsedDown != 300 || got.TotalDown != 300 {
		t.Fatalf("the group's traffic spends the main traffic: used %d total %d", got.UsedDown, got.TotalDown)
	}
}

// A group with a limit of the user's own, on a tariff with nothing to reset, is renewed once
// the user is given a reset of its own (the client's card): the scheduled reset zeroes the
// group, at once when the period the new rule counts is already over. Taking the tariff
// again brings the tariff's reset back; a reset in the same patch wins over it.
func TestOwnResetOfAUser(t *testing.T) {
	s, node, st, users, now := setup(t)
	ctx := context.Background()
	q := st.Q
	pool, err := q.CreateTrafficPool(ctx, db.CreateTrafficPoolParams{Name: "WL", CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	ins, _ := q.ListInbounds(ctx)
	if err := q.SetInboundPool(ctx, db.SetInboundPoolParams{PoolID: sql.NullInt64{Int64: pool.ID, Valid: true}, ID: ins[0].ID}); err != nil {
		t.Fatal(err)
	}
	tariffs, _ := q.ListTariffs(ctx)
	unlimited := tariffs[2]
	if unlimited.TrafficLimit.Valid || unlimited.ResetStrategy != "none" {
		t.Fatalf("the seed's tariff without limits: %+v", unlimited)
	}
	u, err := users.Create(ctx, domain.CreateInput{Name: "a", TariffID: unlimited.ID})
	if err != nil {
		t.Fatal(err)
	}
	// The client's card gives the group a limit of this user's own.
	if err := q.SetUserPool(ctx, db.SetUserPoolParams{UserID: u.ID, PoolID: pool.ID, Mode: domain.PoolOwn, TrafficLimit: sql.NullInt64{Int64: 1000, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	slot, _ := q.GetSlot(ctx, u.SlotID.Int64)
	key := strconv.FormatInt(pool.ID, 10)
	node.batch = nodeapi.Counters{Epoch: "e1", Seq: 1, Pools: map[string]map[string]nodeapi.Traffic{slot.Name: {key: {Down: 900}}}}
	s.pullCounters(ctx)
	used := func() int64 {
		t.Helper()
		ups, err := q.ListUserPools(ctx, u.ID)
		if err != nil || len(ups) != 1 {
			t.Fatalf("the user's groups: %+v %v", ups, err)
		}
		return ups[0].UsedUp + ups[0].UsedDown
	}
	if n := used(); n != 900 {
		t.Fatalf("the group used %d", n)
	}
	// Forty days on, a user without a reset keeps what the group used.
	*now = now.Add(40 * 24 * time.Hour)
	if err := s.m.resetPeriods(ctx, *now); err != nil {
		t.Fatal(err)
	}
	if n := used(); n != 900 {
		t.Fatalf("reset with no reset: %d", n)
	}

	bad := "weekly"
	if _, err := users.Update(ctx, u.ID, domain.Patch{ResetStrategy: &bad}); !errors.As(err, new(*domain.FieldError)) {
		t.Fatalf("a reset the panel does not know: %v", err)
	}
	period := "period"
	if u, err = users.Update(ctx, u.ID, domain.Patch{ResetStrategy: &period}); err != nil || u.ResetStrategy != period {
		t.Fatalf("the user's own reset: %q %v", u.ResetStrategy, err)
	}
	// The period that began forty days ago is over by the new rule: the group is reset now,
	// and the new period goes on from where the first one of 30 days ended.
	if err := s.m.resetPeriods(ctx, *now); err != nil {
		t.Fatal(err)
	}
	if n := used(); n != 0 {
		t.Fatalf("the group after the reset: %d", n)
	}
	if got, _ := q.GetUser(ctx, u.ID); got.PeriodStart != u.PeriodStart+30*86400 {
		t.Fatalf("the period starts at %d, not %d", got.PeriodStart, u.PeriodStart+30*86400)
	}

	if u, err = users.Update(ctx, u.ID, domain.Patch{TariffID: &unlimited.ID}); err != nil || u.ResetStrategy != "none" {
		t.Fatalf("the tariff's reset back: %q %v", u.ResetStrategy, err)
	}
	month := "month_start"
	if u, err = users.Update(ctx, u.ID, domain.Patch{TariffID: &unlimited.ID, ResetStrategy: &month}); err != nil || u.ResetStrategy != month {
		t.Fatalf("the patch's reset over its tariff's: %q %v", u.ResetStrategy, err)
	}
}
