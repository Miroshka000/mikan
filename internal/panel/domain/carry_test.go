package domain

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

// setLifecycle writes the panel's lifecycle rules.
func setLifecycle(t *testing.T, st *store.Store, l Lifecycle) {
	t.Helper()
	if err := settings.Set(context.Background(), settings.New(st.Q), KeyLifecycle, l); err != nil {
		t.Fatal(err)
	}
}

// carryRules are the default rules with the carry-over on or off at cap percent.
func carryRules(on bool, cap int) Lifecycle {
	l := DefaultLifecycle
	l.Carry = CarryRules{On: on, Cap: cap}
	return l
}

// setUsed sets what the user's period counted.
func setUsed(t *testing.T, st *store.Store, id, used int64) {
	t.Helper()
	if _, err := st.DB.ExecContext(context.Background(), "UPDATE users SET used_up = 0, used_down = $1 WHERE id = $2", used, id); err != nil {
		t.Fatal(err)
	}
}

// grantsOf are the user's grants of source, newest first.
func grantsOf(t *testing.T, st *store.Store, id int64, source string) []db.TrafficGrant {
	t.Helper()
	all, err := st.Q.ListUserGrants(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var out []db.TrafficGrant
	for _, g := range all {
		if g.Source == source {
			out = append(out, g)
		}
	}
	return out
}

// renew applies a payment's renewal of user id onto tariff on its own transaction.
func renew(t *testing.T, st *store.Store, users *Users, id, tariff int64, reset bool) db.User {
	t.Helper()
	var u db.User
	if err := st.Tx(context.Background(), func(q *db.Queries) (err error) {
		u, _, err = users.Purchase(context.Background(), q, PurchaseInput{UserID: id, TariffID: tariff, ResetTraffic: reset})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return u
}

// A renewal that starts a new traffic period takes over what the old one left of its base
// quota, as a package of the new period, by the panel's rules or the tariff's own, at most
// the cap's share of the new quota.
func TestRenewalCarriesOver(t *testing.T) {
	yes, no := sql.NullBool{Bool: true, Valid: true}, sql.NullBool{Valid: true}
	for _, c := range []struct {
		name  string
		rules Lifecycle
		own   sql.NullBool // the tariff renewed onto
		used  int64
		reset bool
		to    int // the seed's tariffs: 0 trial (5 GiB, no reset), 1 standard (150 GiB a period), 2 unlimited
		want  int64
	}{
		{"off by default", DefaultLifecycle, sql.NullBool{}, 50 * GiB, true, 1, 0},
		{"what the period left", carryRules(true, 100), sql.NullBool{}, 50 * GiB, true, 1, 100 * GiB},
		{"at most half the new quota", carryRules(true, 50), sql.NullBool{}, 0, true, 1, 75 * GiB},
		{"a period that goes on leaves nothing", carryRules(true, 100), sql.NullBool{}, 50 * GiB, false, 1, 0},
		{"the tariff says yes", carryRules(false, 100), yes, 50 * GiB, true, 1, 100 * GiB},
		{"the tariff says no", carryRules(true, 100), no, 50 * GiB, true, 1, 0},
		{"into an unlimited tariff", carryRules(true, 100), sql.NullBool{}, 50 * GiB, true, 2, 0},
		{"into a smaller quota, at most that quota", carryRules(true, 100), sql.NullBool{}, 0, true, 0, 5 * GiB},
		{"nothing left", carryRules(true, 100), sql.NullBool{}, 150 * GiB, true, 1, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			now := time.Unix(1_800_000_000, 0)
			st, users, _ := setup(t, &now)
			ctx := context.Background()
			tariffs, _ := st.Q.ListTariffs(ctx)
			u, err := users.Create(ctx, CreateInput{Name: "a", TariffID: tariffs[1].ID})
			if err != nil {
				t.Fatal(err)
			}
			setLifecycle(t, st, c.rules)
			if c.own.Valid {
				if err := SetTariffLifecycle(ctx, st.Q, tariffs[c.to].ID, TariffLifecycle{Carry: c.own}); err != nil {
					t.Fatal(err)
				}
			}
			setUsed(t, st, u.ID, c.used)
			now = now.Add(10 * 24 * time.Hour)
			u = renew(t, st, users, u.ID, tariffs[c.to].ID, c.reset)
			got := grantsOf(t, st, u.ID, SourceCarry)
			if c.want == 0 {
				if len(got) != 0 {
					t.Fatalf("carried %+v, want nothing", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("carried %+v, want one package of %d", got, c.want)
			}
			g := got[0]
			if g.Bytes != c.want || g.Remaining != c.want || g.Lifetime != LifetimePeriod || g.PoolID.Valid || g.PaymentID.Valid {
				t.Fatalf("carried %+v, want %d of the main traffic for the period", g, c.want)
			}
			// It ends with the new period: a tariff without resets keeps it until it is used.
			end, ok := NextReset(u, now)
			if ok != g.ExpiresAt.Valid || ok && g.ExpiresAt.Int64 != end.Unix() {
				t.Fatalf("carried until %v, the new period ends %v (%v)", g.ExpiresAt, end, ok)
			}
			if u.UsedUp+u.UsedDown != 0 {
				t.Fatalf("the new period counted %d", u.UsedUp+u.UsedDown)
			}
			left, _ := UserGrantsLeft(ctx, st.Q, u.ID, now)
			if left.Main(u.ID) != c.want {
				t.Fatalf("grants left %d, want the carried %d", left.Main(u.ID), c.want)
			}
		})
	}
}

// The packages are not the period's quota: a subscriber past the quota on a package carries
// nothing over and keeps the package; and what was carried is a package itself, so the next
// renewal does not carry it again: it ends with its period, and only that period's quota
// left goes on.
func TestCarriedIsNotCarriedAgain(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, users, _ := setup(t, &now)
	ctx := context.Background()
	tariffs, _ := st.Q.ListTariffs(ctx)
	std := tariffs[1]
	u, err := users.Create(ctx, CreateInput{Name: "a", TariffID: std.ID})
	if err != nil {
		t.Fatal(err)
	}
	setLifecycle(t, st, carryRules(true, 100))
	bonus, err := GrantTx(ctx, st.Q, u, GrantSpec{Bytes: 20 * GiB, Lifetime: LifetimeUsed, Source: SourceAdmin}, now)
	if err != nil {
		t.Fatal(err)
	}
	setUsed(t, st, u.ID, 160*GiB) // the quota and 10 GiB of the package
	u = renew(t, st, users, u.ID, std.ID, true)
	if got := grantsOf(t, st, u.ID, SourceCarry); len(got) != 0 {
		t.Fatalf("a quota used up carried %+v", got)
	}
	if g := grantsOf(t, st, u.ID, SourceAdmin); len(g) != 1 || g[0].ID != bonus.ID || !GrantActive(g[0], now) {
		t.Fatalf("the package after the renewal: %+v", g)
	}

	// A period nothing was used of carries its whole quota; the next renewal ends that
	// package with its period and carries the new period's quota alone.
	now = now.Add(time.Hour)
	setUsed(t, st, u.ID, 0)
	u = renew(t, st, users, u.ID, std.ID, true)
	now = now.Add(time.Hour)
	u = renew(t, st, users, u.ID, std.ID, true)
	var active []db.TrafficGrant
	for _, g := range grantsOf(t, st, u.ID, SourceCarry) {
		if GrantActive(g, now) {
			active = append(active, g)
		}
	}
	if len(active) != 1 || active[0].Bytes != 150*GiB {
		t.Fatalf("carried packages running: %+v, want one of the last period's 150 GiB", active)
	}
}
