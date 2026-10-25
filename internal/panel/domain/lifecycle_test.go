package domain

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"testing"
	"time"

	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

func quota(n int64) sql.NullInt64 { return sql.NullInt64{Int64: n, Valid: true} }

// What carries is the base quota a period leaves, never more than the cap's share of the new
// quota, nothing from or into a period without a limit, and nothing too small to be worth a
// package of its own.
func TestCarried(t *testing.T) {
	const mib = int64(1 << 20)
	for _, c := range []struct {
		name        string
		limit, next sql.NullInt64
		used        int64
		cap         int
		want        int64
	}{
		{"what is left", quota(100 * GiB), quota(100 * GiB), 30 * GiB, 100, 70 * GiB},
		{"capped at half the new quota", quota(100 * GiB), quota(100 * GiB), 10 * GiB, 50, 50 * GiB},
		{"capped at the new quota", quota(500 * GiB), quota(100 * GiB), 0, 100, 100 * GiB},
		{"the cap is the new quota's", quota(10 * GiB), quota(200 * GiB), 0, 10, 10 * GiB},
		{"nothing left", quota(100 * GiB), quota(100 * GiB), 100 * GiB, 100, 0},
		{"past the limit on packages", quota(100 * GiB), quota(100 * GiB), 120 * GiB, 100, 0},
		{"an unlimited period leaves nothing to count", sql.NullInt64{}, quota(100 * GiB), 0, 100, 0},
		{"an unlimited new period needs nothing", quota(100 * GiB), sql.NullInt64{}, 0, 100, 0},
		{"no cap carries nothing", quota(100 * GiB), quota(100 * GiB), 0, 0, 0},
		{"less than a mebibyte stays", quota(100 * GiB), quota(100 * GiB), 100*GiB - mib + 1, 100, 0},
		{"a mebibyte carries", quota(100 * GiB), quota(100 * GiB), 100*GiB - mib, 100, mib},
		{"a quota near the top does not overflow", quota(math.MaxInt64), quota(math.MaxInt64), 0, 100, math.MaxInt64/100*100 + math.MaxInt64%100},
	} {
		if got := Carried(c.limit, c.used, c.next, c.cap); got != c.want {
			t.Errorf("%s: carried %d, want %d", c.name, got, c.want)
		}
	}
}

// A tariff's own word decides over the panel's.
func TestCarriesOver(t *testing.T) {
	yes, no := sql.NullBool{Bool: true, Valid: true}, sql.NullBool{Valid: true}
	for _, c := range []struct {
		panel bool
		own   sql.NullBool
		want  bool
	}{
		{false, sql.NullBool{}, false},
		{true, sql.NullBool{}, true},
		{false, yes, true},
		{true, no, false},
		{true, yes, true},
		{false, no, false},
	} {
		if got := (CarryRules{On: c.panel, Cap: 100}).CarriesOver(TariffLifecycle{Carry: c.own}); got != c.want {
			t.Errorf("panel %v, tariff %+v: %v, want %v", c.panel, c.own, got, c.want)
		}
	}
}

func TestLifecycleValidate(t *testing.T) {
	ok := DefaultLifecycle
	for _, c := range []struct {
		name        string
		l           Lifecycle
		field, code string
	}{
		{"the default", ok, "", ""},
		{"the widest", Lifecycle{Carry: CarryRules{On: true, Cap: 100}, Removal: RemovalRules{On: true, Days: MaxRemovalDays}}, "", ""},
		{"the narrowest", Lifecycle{Carry: CarryRules{Cap: 1}, Removal: RemovalRules{Days: MinRemovalDays}}, "", ""},
		{"no cap", Lifecycle{Carry: CarryRules{On: true}, Removal: ok.Removal}, "carry.cap_percent", "bad_carry_cap"},
		{"over a whole quota", Lifecycle{Carry: CarryRules{Cap: 101}, Removal: ok.Removal}, "carry.cap_percent", "bad_carry_cap"},
		{"a negative cap", Lifecycle{Carry: CarryRules{Cap: -5}, Removal: ok.Removal}, "carry.cap_percent", "bad_carry_cap"},
		{"removed within the week", Lifecycle{Carry: ok.Carry, Removal: RemovalRules{On: true, Days: MinRemovalDays - 1}}, "removal.days", "bad_removal_days"},
		{"no days", Lifecycle{Carry: ok.Carry, Removal: RemovalRules{}}, "removal.days", "bad_removal_days"},
		{"past ten years", Lifecycle{Carry: ok.Carry, Removal: RemovalRules{Days: MaxRemovalDays + 1}}, "removal.days", "bad_removal_days"},
	} {
		err := c.l.Validate()
		var fe *FieldError
		switch {
		case c.code == "" && err != nil:
			t.Errorf("%s: refused: %v", c.name, err)
		case c.code != "" && (!errors.As(err, &fe) || fe.Field != c.field || fe.Code != c.code):
			t.Errorf("%s: %v, want %s at %s", c.name, err, c.code, c.field)
		}
	}
}

// Unset, the rules are the default; stored, they are what was stored, a part the JSON lacks
// keeping the default's; stored broken or out of bounds, the default again, which carries and
// removes nothing.
func TestLoadLifecycle(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, _, _ := setup(t, &now)
	ctx := context.Background()
	set := settings.New(st.Q)
	load := func() Lifecycle {
		t.Helper()
		l, err := LoadLifecycle(ctx, set)
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	if got := load(); got != DefaultLifecycle {
		t.Fatalf("unset: %+v", got)
	}
	mine := Lifecycle{Carry: CarryRules{On: true, Cap: 40}, Removal: RemovalRules{On: true, Days: 60}}
	if err := settings.Set(ctx, set, KeyLifecycle, mine); err != nil {
		t.Fatal(err)
	}
	if got := load(); got != mine {
		t.Fatalf("stored: %+v", got)
	}
	for raw, want := range map[string]Lifecycle{
		`{"carry":{"on":true,"cap_percent":25}}`: {Carry: CarryRules{On: true, Cap: 25}, Removal: DefaultLifecycle.Removal},
		`{"carry":{"on":true,"cap_percent":0}}`:  DefaultLifecycle,
		`{"removal":{"on":true,"days":1}}`:       DefaultLifecycle,
		`not json`:                               DefaultLifecycle,
	} {
		if err := st.Q.SetSetting(ctx, db.SetSettingParams{Key: KeyLifecycle, Value: raw}); err != nil {
			t.Fatal(err)
		}
		if got := load(); got != want {
			t.Errorf("%s: %+v, want %+v", raw, got, want)
		}
	}
}

// A tariff without its own part has the panel's; written, it reads back, in the list too.
func TestTariffLifecycle(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, _, _ := setup(t, &now)
	ctx := context.Background()
	tariffs, _ := st.Q.ListTariffs(ctx)
	id := tariffs[1].ID
	if tl, err := LoadTariffLifecycle(ctx, st.Q, id); err != nil || tl != (TariffLifecycle{}) {
		t.Fatalf("none yet: %+v %v", tl, err)
	}
	own := TariffLifecycle{Carry: sql.NullBool{Bool: true, Valid: true}, StartOnConnect: true}
	if err := SetTariffLifecycle(ctx, st.Q, id, own); err != nil {
		t.Fatal(err)
	}
	if tl, err := LoadTariffLifecycle(ctx, st.Q, id); err != nil || tl != own {
		t.Fatalf("written: %+v %v", tl, err)
	}
	back := TariffLifecycle{}
	if err := SetTariffLifecycle(ctx, st.Q, id, back); err != nil {
		t.Fatal(err)
	}
	all, err := TariffLifecycles(ctx, st.Q)
	if err != nil || len(all) != 1 || all[id] != back {
		t.Fatalf("the list: %+v %v", all, err)
	}
}
