package nodesync

import (
	"context"
	"database/sql"
	"strconv"
	"testing"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
)

// Traffic packages on the panel's side: counters past the base quota spend the grants
// once even when the node delivers the batch again, a used-up user and pool come back
// after a grant and the node's quota grows by it, and the period reset keeps grants.
func TestTrafficGrantsOnThePanel(t *testing.T) {
	s, node, st, users, _ := setup(t)
	s.m.storeInterval = 0 // batch after batch, each stored
	ctx := context.Background()
	q := st.Q
	pool, err := q.CreateTrafficPool(ctx, db.CreateTrafficPoolParams{Name: "WL", CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	tariffs, _ := q.ListTariffs(ctx)
	u, err := users.Create(ctx, domain.CreateInput{Name: "a", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	limit := 10 * domain.GiB
	if u, err = users.Update(ctx, u.ID, domain.Patch{TrafficLimit: &limit}); err != nil {
		t.Fatal(err)
	}
	if err := q.SetUserPoolLimit(ctx, db.SetUserPoolLimitParams{UserID: u.ID, PoolID: pool.ID, TrafficLimit: sql.NullInt64{Int64: domain.GiB, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	slot, _ := q.GetSlot(ctx, u.SlotID.Int64)
	key := strconv.FormatInt(pool.ID, 10)
	policy := func() nodeapi.Policy {
		t.Helper()
		_, ps, _, err := s.policies(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range ps {
			if p.Slot == slot.Name {
				return p
			}
		}
		t.Fatal("no policy")
		return nodeapi.Policy{}
	}
	grant := func(pool int64, gb int64) {
		t.Helper()
		if _, err := users.Grant(ctx, u.ID, domain.GrantInput{PoolID: pool, Bytes: gb * domain.GiB, Lifetime: domain.LifetimeUsed}); err != nil {
			t.Fatal(err)
		}
	}
	grant(0, 5)
	if p := policy(); p.QuotaRemaining != 15*domain.GiB {
		t.Fatalf("quota with a 5 GB package: %d", p.QuotaRemaining)
	}

	// 12 GB: 10 of the base and 2 of the package. The node sends the batch twice.
	node.batch = nodeapi.Counters{Epoch: "e1", Seq: 1, Slots: map[string]nodeapi.Traffic{slot.Name: {Down: 12 * domain.GiB}},
		Pools: map[string]map[string]nodeapi.Traffic{slot.Name: {key: {Down: 3 * domain.GiB}}}}
	s.pullCounters(ctx)
	s.pullCounters(ctx)
	left, _ := domain.UserGrantsLeft(ctx, q, u.ID, s.m.now())
	if left.Main(u.ID) != 3*domain.GiB {
		t.Fatalf("main package left %d, want 3 GB (spent once)", left.Main(u.ID))
	}
	p := policy()
	if p.QuotaRemaining != 3*domain.GiB || len(p.Pools) != 1 || p.Pools[0].Remaining != 0 || !p.Allowed {
		t.Fatalf("after the batch: %+v", p)
	}

	// The rest of the package and 1 GB more: limited, quota 0.
	node.batch = nodeapi.Counters{Epoch: "e1", Seq: 2, Slots: map[string]nodeapi.Traffic{slot.Name: {Down: 4 * domain.GiB}}}
	s.pullCounters(ctx)
	got, _ := q.GetUser(ctx, u.ID)
	if st := domain.State(got, 0, s.m.now()); st != domain.StateLimited || policy().QuotaRemaining != 0 {
		t.Fatalf("all used: %s quota %d", st, policy().QuotaRemaining)
	}

	// A grant for the main traffic and one for the pool: back at once, the node is told.
	pushes := len(node.policies)
	grant(0, 2)
	grant(pool.ID, 1)
	s.pushPolicies(ctx, true) // what the PoliciesChanged signal makes the syncer do
	if len(node.policies) <= pushes {
		t.Fatal("no push after the grant")
	}
	pushed := node.policies[len(node.policies)-1]
	var mine nodeapi.Policy
	for _, x := range pushed {
		if x.Slot == slot.Name {
			mine = x
		}
	}
	if mine.QuotaRemaining != 2*domain.GiB || len(mine.Pools) != 1 || mine.Pools[0].Remaining != domain.GiB {
		t.Fatalf("pushed after the grants: %+v", mine)
	}
	left, _ = domain.UserGrantsLeft(ctx, q, u.ID, s.m.now())
	if st := domain.State(got, left.Main(u.ID), s.m.now()); st != domain.StateActive {
		t.Fatalf("after the grant: %s", st)
	}
	spent, _ := domain.ExhaustedPools(ctx, q, u.ID, mustLeft(t, ctx, q, u.ID, s.m.now()))
	if spent[pool.ID] {
		t.Fatal("the pool is back after its grant")
	}

	// The period reset: the base is whole again and the packages keep what is left.
	if _, err := users.ResetTraffic(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if p := policy(); p.QuotaRemaining != 12*domain.GiB || p.Pools[0].Remaining != 2*domain.GiB {
		t.Fatalf("after the reset: quota %d pool %+v", p.QuotaRemaining, p.Pools)
	}
}

// mustLeft is what is left of the user's grants now.
func mustLeft(t *testing.T, ctx context.Context, q *db.Queries, userID int64, now time.Time) domain.GrantsLeft {
	t.Helper()
	left, err := domain.UserGrantsLeft(ctx, q, userID, now)
	if err != nil {
		t.Fatal(err)
	}
	return left
}
