package domain

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"mikan/internal/panel/store/db"
)

// Pool limits set on a tariff after it was given out reach its users when the admin says
// so: the tariff's limit replaces their own, what they used stays, other tariffs' users
// are left alone.
func TestApplyTariffPoolsToAll(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, users, ch := setup(t, &now)
	ctx := context.Background()
	tariffs, _ := st.Q.ListTariffs(ctx)
	tr, other := tariffs[0], tariffs[1]
	a, _ := users.Create(ctx, CreateInput{Name: "a", TariffID: tr.ID})
	b, _ := users.Create(ctx, CreateInput{Name: "b", TariffID: other.ID})
	var pool int64
	if err := st.DB.QueryRowContext(ctx, "INSERT INTO traffic_pools(name,created_at) VALUES('WL',1) RETURNING id").Scan(&pool); err != nil {
		t.Fatal(err)
	}
	// Given out before the limit: no WL limit, so no WL packages.
	if err := st.Q.SetUserPoolLimit(ctx, db.SetUserPoolLimitParams{UserID: a.ID, PoolID: pool, TrafficLimit: sql.NullInt64{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, "UPDATE user_pools SET used_up = 7 WHERE user_id = $1", a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO tariff_pools(tariff_id,pool_id,traffic_limit,excluded) VALUES($1,$2,$3,false)", tr.ID, pool, int64(100)<<30); err != nil {
		t.Fatal(err)
	}
	n, err := users.ApplyTariffPoolsToAll(ctx, tr.ID)
	if err != nil || n != 1 {
		t.Fatalf("applied to %d: %v", n, err)
	}
	ps, _ := st.Q.ListUserPools(ctx, a.ID)
	if len(ps) != 1 || !ps[0].TrafficLimit.Valid || ps[0].TrafficLimit.Int64 != int64(100)<<30 || ps[0].UsedUp != 7 {
		t.Fatalf("a's pools: %+v", ps)
	}
	if ps, _ := st.Q.ListUserPools(ctx, b.ID); len(ps) != 0 {
		t.Fatalf("another tariff's user changed: %+v", ps)
	}
	if ch.policies == 0 {
		t.Fatal("the nodes are not told")
	}
	if _, err := users.ApplyTariffPoolsToAll(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no such tariff: %v", err)
	}
}
