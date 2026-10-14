package nodesync

import (
	"context"
	"database/sql"
	"slices"
	"strconv"
	"testing"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
)

// A pool the tariff leaves out: the user gets it closed, nothing of it to buy, and the
// node lets the user into every inbound but the pool's.
func TestExcludedPoolOnTheNode(t *testing.T) {
	s, _, st, users, now := setup(t)
	ctx := context.Background()
	q := st.Q
	pool, err := q.CreateTrafficPool(ctx, db.CreateTrafficPoolParams{Name: "Premium", CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	ins, _ := q.ListInbounds(ctx)
	premium := ins[0]
	if err := q.SetInboundPool(ctx, db.SetInboundPoolParams{PoolID: sql.NullInt64{Int64: pool.ID, Valid: true}, ID: premium.ID}); err != nil {
		t.Fatal(err)
	}
	tariffs, _ := q.ListTariffs(ctx)
	if err := q.AddTariffPool(ctx, db.AddTariffPoolParams{TariffID: tariffs[1].ID, PoolID: pool.ID, Excluded: true}); err != nil {
		t.Fatal(err)
	}
	cheap, _ := users.Create(ctx, domain.CreateInput{Name: "cheap", TariffID: tariffs[1].ID})
	rich, _ := users.Create(ctx, domain.CreateInput{Name: "rich", TariffID: tariffs[2].ID})

	ups, _ := q.ListUserPools(ctx, cheap.ID)
	if len(ups) != 1 || !ups[0].Excluded || ups[0].TrafficLimit.Valid {
		t.Fatalf("closed by the tariff, without a limit: %+v", ups)
	}
	if shut, _ := domain.ExhaustedPools(ctx, q, cheap.ID, mustLeft(t, ctx, q, cheap.ID, *now)); !shut[pool.ID] {
		t.Fatal("a closed pool does not serve: the subscription leaves it out")
	}
	if domain.PackageFits(cheap, ups, db.TrafficPackage{PoolID: sql.NullInt64{Int64: pool.ID, Valid: true}}) {
		t.Fatal("nothing of a closed pool is sold")
	}

	byUser := map[int64]nodeapi.Policy{}
	_, ps, owners, err := s.policies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		byUser[owners[p.Slot]] = p
	}
	p := byUser[cheap.ID]
	var want []string
	for _, in := range ins {
		if in.NodeID == LocalNode && in.ID != premium.ID {
			want = append(want, in.Name)
		}
	}
	slices.Sort(want)
	got := slices.Clone(p.Inbounds)
	slices.Sort(got)
	if !p.Allowed || !slices.Equal(got, want) {
		t.Fatalf("every inbound here but the closed pool's: %v, want %v", got, want)
	}
	if r := byUser[rich.ID]; !r.Allowed || len(r.Inbounds) != 0 {
		t.Fatalf("another tariff keeps all: %+v", r)
	}

	// Closed per user on top of a list the admin chose: the list loses the pool too.
	raw := "[" + strconv.FormatInt(premium.ID, 10) + "]"
	if _, err := st.DB.ExecContext(ctx, "UPDATE users SET inbounds = $1 WHERE id = $2", raw, cheap.ID); err != nil {
		t.Fatal(err)
	}
	s.m.PoliciesChanged()
	_, ps, owners, _ = s.policies(ctx)
	for _, p := range ps {
		if owners[p.Slot] == cheap.ID && p.Allowed {
			t.Fatalf("only the closed pool allowed: no way in here: %+v", p)
		}
	}
}
