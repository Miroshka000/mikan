package domain

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	"mikan/internal/panel/events"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

// switchesOf are the user.switched events of the feed, oldest first.
func switchesOf(t *testing.T, st *store.Store) []events.Switched {
	t.Helper()
	var out []events.Switched
	for _, e := range feedOf(t, st) {
		if e.Type != events.UserSwitched {
			continue
		}
		var s events.Switched
		if err := json.Unmarshal([]byte(e.Data), &s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// What the next tariff may be: a tariff on offer, a term up to ten years, for a subscription
// that has an end to follow.
func TestSetNextTariffRefuses(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, users, _ := setup(t, &now)
	ctx := context.Background()
	tariffs, _ := st.Q.ListTariffs(ctx)
	u, err := users.Create(ctx, CreateInput{Name: "a", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	endless, err := users.Create(ctx, CreateInput{Name: "endless", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := users.Update(ctx, endless.ID, Patch{ClearExpiry: true}); err != nil {
		t.Fatal(err)
	}
	old, err := st.Q.CreateTariff(ctx, smallTariff("old", 30))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Q.ArchiveTariff(ctx, old.ID); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name        string
		user        int64
		next        NextTariff
		field, code string
	}{
		{"an unknown tariff", u.ID, NextTariff{TariffID: 9999}, "tariff_id", "tariff_not_found"},
		{"an archived tariff", u.ID, NextTariff{TariffID: old.ID}, "tariff_id", "tariff_not_found"},
		{"no end to follow", endless.ID, NextTariff{TariffID: tariffs[2].ID}, "tariff_id", "no_term_end"},
		{"a negative term", u.ID, NextTariff{TariffID: tariffs[2].ID, TermDays: sql.NullInt64{Int64: -1, Valid: true}}, "term_days", "bad_days"},
		{"past ten years", u.ID, NextTariff{TariffID: tariffs[2].ID, TermDays: sql.NullInt64{Int64: MaxExtendDays + 1, Valid: true}}, "term_days", "bad_days"},
	} {
		_, _, err := users.SetNextTariff(ctx, c.user, c.next)
		var fe *FieldError
		if !errors.As(err, &fe) || fe.Field != c.field || fe.Code != c.code {
			t.Errorf("%s: %v, want %s at %s", c.name, err, c.code, c.field)
		}
	}
	if _, _, err := users.SetNextTariff(ctx, 9999, NextTariff{TariffID: tariffs[2].ID}); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown user: %v", err)
	}
	if _, ok, err := NextTariffOf(ctx, st.Q, u.ID); err != nil || ok {
		t.Fatalf("a refused next tariff was kept: %v %v", ok, err)
	}
}

// smallTariff is a tariff of 10 GiB a period, of days.
func smallTariff(name string, days int64) db.CreateTariffParams {
	return db.CreateTariffParams{Name: name, TrafficLimit: quota(10 * GiB), DurationDays: days, ResetStrategy: "period", CreatedAt: 1_800_000_000}
}

// The next tariff takes over once, in the round before the term ends: the tariff's limits, a
// term of its own from the old term's end (nothing lost or gained by the round being early),
// a new traffic period with what the old one left by the carry-over rules; the subscription
// stays off when the admin had switched it off, the journal and the addons hear of it, and
// it does not come again.
func TestNextTariffTakesOverOnce(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, users, ch := setup(t, &now)
	ctx := context.Background()
	tariffs, _ := st.Q.ListTariffs(ctx)
	std := tariffs[1]
	small, err := st.Q.CreateTariff(ctx, smallTariff("small", 14))
	if err != nil {
		t.Fatal(err)
	}
	u, err := users.Create(ctx, CreateInput{Name: "a", TariffID: std.ID})
	if err != nil {
		t.Fatal(err)
	}
	setLifecycle(t, st, carryRules(true, 50))
	end := u.ExpiresAt.Int64
	days := sql.NullInt64{Int64: 7, Valid: true}
	if _, switched, err := users.SetNextTariff(ctx, u.ID, NextTariff{TariffID: small.ID, TermDays: days}); err != nil || switched {
		t.Fatalf("set: %v %v", switched, err)
	}
	if n, ok, err := NextTariffOf(ctx, st.Q, u.ID); err != nil || !ok || n.TariffID != small.ID || n.TermDays != days {
		t.Fatalf("kept: %+v %v %v", n, ok, err)
	}
	off := true
	if _, err := users.Update(ctx, u.ID, Patch{Disabled: &off}); err != nil {
		t.Fatal(err)
	}
	setUsed(t, st, u.ID, 145*GiB)

	// A round well before the end leaves it be; the round before the end switches it.
	now = time.Unix(end, 0).Add(-SwitchAhead - time.Minute)
	if n, err := users.SwitchDue(ctx); err != nil || n != 0 {
		t.Fatalf("too early: %d %v", n, err)
	}
	now = time.Unix(end, 0).Add(-SwitchAhead + time.Minute)
	pushes := ch.policies
	if n, err := users.SwitchDue(ctx); err != nil || n != 1 {
		t.Fatalf("switch: %d %v", n, err)
	}
	if ch.policies != pushes+1 {
		t.Fatal("the nodes were not told of the new limits")
	}
	got, _ := users.Get(ctx, u.ID)
	if got.TariffID.Int64 != small.ID || got.TrafficLimit != small.TrafficLimit || got.ResetStrategy != "period" || got.Status != "disabled" {
		t.Fatalf("switched: %+v", got)
	}
	if want := end + 7*day; got.ExpiresAt.Int64 != want {
		t.Fatalf("expires %d, want the old end and 7 days, %d", got.ExpiresAt.Int64, want)
	}
	if got.UsedUp+got.UsedDown != 0 || got.PeriodStart != now.Unix() {
		t.Fatalf("the new period: used %d from %d", got.UsedUp+got.UsedDown, got.PeriodStart)
	}
	// 5 GiB were left, half the new 10 GiB quota is the cap.
	if c := grantsOf(t, st, u.ID, SourceCarry); len(c) != 1 || c[0].Bytes != 5*GiB {
		t.Fatalf("carried %+v", c)
	}
	if _, ok, _ := NextTariffOf(ctx, st.Q, u.ID); ok {
		t.Fatal("the next tariff is still there")
	}
	told := switchesOf(t, st)
	if len(told) != 1 || told[0] != (events.Switched{TariffID: small.ID, FromTariffID: std.ID, ExpiresAt: got.ExpiresAt.Int64, Carried: 5 * GiB}) {
		t.Fatalf("told %+v", told)
	}
	var lines int
	if err := st.DB.QueryRowContext(ctx, "SELECT count(*) FROM audit_log WHERE action = 'user.switch_tariff' AND target_id = $1", strconv.FormatInt(u.ID, 10)).Scan(&lines); err != nil || lines != 1 {
		t.Fatalf("journal lines %d %v", lines, err)
	}
	// Once: the next round, and the end of the new term, switch nothing.
	now = time.Unix(got.ExpiresAt.Int64, 0)
	if n, err := users.SwitchDue(ctx); err != nil || n != 0 {
		t.Fatalf("again: %d %v", n, err)
	}
}

// A renewal that comes first pushes the end on, and the next tariff waits for that one; one
// taken away never comes; a term that waits for its first connection does not end; and a
// subscription already over when the next tariff is set switches at once, from now.
func TestNextTariffWaitsForTheEnd(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, users, _ := setup(t, &now)
	ctx := context.Background()
	tariffs, _ := st.Q.ListTariffs(ctx)
	std, unl := tariffs[1], tariffs[2]
	mk := func(name string, wait bool) int64 {
		t.Helper()
		u, err := users.Create(ctx, CreateInput{Name: name, TariffID: std.ID, StartsOnConnect: &wait})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := users.SetNextTariff(ctx, u.ID, NextTariff{TariffID: unl.ID}); err != nil {
			t.Fatal(err)
		}
		return u.ID
	}
	renewed, cleared, waits := mk("renewed", false), mk("cleared", false), mk("waits", true)
	renew(t, st, users, renewed, std.ID, true)
	if err := users.ClearNextTariff(ctx, cleared); err != nil {
		t.Fatal(err)
	}
	now = now.Add(30*24*time.Hour - SwitchAhead/2)
	if n, err := users.SwitchDue(ctx); err != nil || n != 0 {
		t.Fatalf("switched %d %v, want none", n, err)
	}
	for _, id := range []int64{renewed, waits} {
		if _, ok, _ := NextTariffOf(ctx, st.Q, id); !ok {
			t.Fatalf("user %d lost its next tariff", id)
		}
	}
	if err := users.ClearNextTariff(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown user: %v", err)
	}

	over, err := users.Create(ctx, CreateInput{Name: "over", TariffID: std.ID})
	if err != nil {
		t.Fatal(err)
	}
	past := now.Add(-48 * time.Hour)
	if _, err := users.Update(ctx, over.ID, Patch{ExpiresAt: &past}); err != nil {
		t.Fatal(err)
	}
	u, switched, err := users.SetNextTariff(ctx, over.ID, NextTariff{TariffID: unl.ID})
	if err != nil || !switched || u.TariffID.Int64 != unl.ID || u.ExpiresAt.Int64 != now.Add(30*24*time.Hour).Unix() {
		t.Fatalf("over: switched %v, %+v %v", switched, u, err)
	}
}
