package domain

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"mikan/internal/panel/store/db"
)

// A bulk action is one transaction: when it fails on a user, the users before it are as
// they were (a loop of single changes left them changed and unrecorded).
func TestBulkIsAllOrNothing(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, users, ch := setup(t, &now)
	ctx := context.Background()
	tariffs, _ := st.Q.ListTariffs(ctx)
	var ids []int64
	for _, name := range []string{"a", "b", "c"} {
		u, err := users.Create(ctx, CreateInput{Name: name, TariffID: tariffs[1].ID})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, u.ID)
	}
	expiry := func(id int64) int64 {
		u, _ := st.Q.GetUser(ctx, id)
		return u.ExpiresAt.Int64
	}
	before := []int64{expiry(ids[0]), expiry(ids[1]), expiry(ids[2])}
	pushes := ch.policies

	// The third user cannot be written: the first two must not stay extended.
	if _, err := st.DB.ExecContext(ctx, "ALTER TABLE users ADD CONSTRAINT no_third CHECK(id <> "+strconv.FormatInt(ids[2], 10)+" OR expires_at = "+strconv.FormatInt(before[2], 10)+")"); err != nil {
		t.Fatal(err)
	}
	if _, err := users.Bulk(ctx, ids, BulkExtend, BulkOpt{Days: 10}); err == nil {
		t.Fatal("the bulk change did not fail")
	}
	for i, id := range ids {
		if got := expiry(id); got != before[i] {
			t.Fatalf("user %d was changed by a bulk that failed: %d, was %d", i, got, before[i])
		}
	}
	if ch.policies != pushes {
		t.Fatal("nodes were told about a change that did not happen")
	}
	if _, err := st.DB.ExecContext(ctx, "ALTER TABLE users DROP CONSTRAINT no_third"); err != nil {
		t.Fatal(err)
	}

	// Gone users are skipped, a user named twice is changed once.
	n, err := users.Bulk(ctx, []int64{ids[0], 9999, ids[0], ids[1]}, BulkExtend, BulkOpt{Days: 10})
	if err != nil || n != 2 {
		t.Fatalf("bulk: %d changed, %v, want 2", n, err)
	}
	if got, want := expiry(ids[0]), before[0]+10*86400; got != want {
		t.Fatalf("user listed twice: expiry %d, want %d (once)", got, want)
	}
	if ch.policies != pushes+1 {
		t.Fatalf("policy pushes %d, want one for the whole list", ch.policies-pushes)
	}
	if n, err := users.Bulk(ctx, ids, BulkDelete, BulkOpt{}); err != nil || n != 3 {
		t.Fatalf("delete: %d %v", n, err)
	}
	if _, err := st.Q.GetUser(ctx, ids[1]); err == nil {
		t.Fatal("a deleted user is still there")
	}
	if _, err := users.Bulk(ctx, ids, "explode", BulkOpt{}); err == nil {
		t.Fatal("an unknown action was accepted")
	}
}

// The plan of a bulk action is what the action then does: of the users asked for, each once,
// how many are there and how many it changes at all, the first of those with the end an
// extension gives them, and for a deletion what the users deleted hold; with the action's own
// refusals.
func TestBulkPlan(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, users, _ := setup(t, &now)
	ctx := context.Background()
	tariffs, _ := st.Q.ListTariffs(ctx)
	var ids []int64
	for i := range 12 {
		u, err := users.Create(ctx, CreateInput{Name: "u" + strconv.Itoa(i), TariffID: tariffs[1].ID})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, u.ID)
	}
	off := true
	if _, err := users.Update(ctx, ids[1], Patch{Disabled: &off}); err != nil {
		t.Fatal(err)
	}
	asked := []int64{ids[2], ids[0], ids[1], ids[2], 9999}

	plan, err := users.BulkPlan(ctx, asked, BulkDisable, BulkOpt{})
	if err != nil || plan.Asked != 4 || plan.Found != 3 || plan.Changed != 2 || plan.AtStake != nil {
		t.Fatalf("disable: %+v %v", plan, err)
	}
	if len(plan.Sample) != 2 || plan.Sample[0].User.ID != ids[0] || plan.Sample[1].User.ID != ids[2] {
		t.Fatalf("disable's sample: %+v, want the two still on, in id order", plan.Sample)
	}
	if plan, err = users.BulkPlan(ctx, asked, BulkEnable, BulkOpt{}); err != nil || plan.Changed != 1 || plan.Sample[0].User.ID != ids[1] {
		t.Fatalf("enable: %+v %v", plan, err)
	}

	// The extension planned is the extension done, from each user's own end.
	plan, err = users.BulkPlan(ctx, asked, BulkExtend, BulkOpt{Days: 10})
	if err != nil || plan.Changed != 3 || len(plan.Sample) != 3 {
		t.Fatalf("extend: %+v %v", plan, err)
	}
	if _, err := users.Bulk(ctx, asked, BulkExtend, BulkOpt{Days: 10}); err != nil {
		t.Fatal(err)
	}
	for _, c := range plan.Sample {
		u, _ := users.Get(ctx, c.User.ID)
		if u.ExpiresAt != c.Expires || c.Expires.Int64 != c.User.ExpiresAt.Int64+10*day {
			t.Fatalf("user %d: planned %v, done %v", c.User.ID, c.Expires, u.ExpiresAt)
		}
	}

	// A new period for all of them, ten shown; a deletion counts what goes with the users.
	if plan, err = users.BulkPlan(ctx, ids, BulkReset, BulkOpt{}); err != nil || plan.Changed != 12 || len(plan.Sample) != BulkSampleSize {
		t.Fatalf("reset: %d changed, %d shown, %v", plan.Changed, len(plan.Sample), err)
	}
	if err := st.Q.LinkTg(ctx, db.LinkTgParams{UserID: ids[0], TgID: 7, CreatedAt: now.Unix()}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO balances (tg_id, amount, updated_at) VALUES (7, 100, $1)", now.Unix()); err != nil {
		t.Fatal(err)
	}
	u3, _ := users.Get(ctx, ids[3])
	if _, err := GrantTx(ctx, st.Q, u3, GrantSpec{Bytes: GiB, Lifetime: LifetimeUsed, Source: SourceAdmin}, now); err != nil {
		t.Fatal(err)
	}
	plan, err = users.BulkPlan(ctx, ids, BulkDelete, BulkOpt{})
	if err != nil || plan.Changed != 12 || plan.AtStake == nil || plan.AtStake.WithBalance != 1 || plan.AtStake.Granted != 1 || plan.AtStake.Paying != 0 {
		t.Fatalf("delete: %+v %v", plan, err)
	}
	if n, _ := users.Bulk(ctx, ids, BulkReset, BulkOpt{}); n != 12 {
		t.Fatal("the plans changed someone")
	}

	if _, err := users.BulkPlan(ctx, ids, "explode", BulkOpt{}); err == nil {
		t.Fatal("an unknown action was planned")
	}
	var fe *FieldError
	if _, err := users.BulkPlan(ctx, ids, BulkExtend, BulkOpt{Days: MaxExtendDays + 1}); !errors.As(err, &fe) || fe.Code != "bad_days" {
		t.Fatalf("an extension past ten years: %v", err)
	}
}

// Each bulk action does to every listed user what the single change does.
func TestBulkActions(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, users, ch := setup(t, &now)
	ctx := context.Background()
	clock := func() time.Time { return now }
	devs := NewDevices(st, NewPool(st, clock), ch, clock)
	tariffs, _ := st.Q.ListTariffs(ctx)
	var ids []int64
	for _, name := range []string{"a", "b"} {
		u, err := users.Create(ctx, CreateInput{Name: name, TariffID: tariffs[1].ID})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, u.ID)
	}
	get := func(id int64) db.User {
		u, err := st.Q.GetUser(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	if n, err := users.Bulk(ctx, ids, BulkDisable, BulkOpt{}); err != nil || n != 2 || get(ids[0]).Status != "disabled" || get(ids[1]).Status != "disabled" {
		t.Fatalf("disable: %d %v", n, err)
	}
	if n, err := users.Bulk(ctx, ids, BulkEnable, BulkOpt{}); err != nil || n != 2 || get(ids[0]).Status != "active" {
		t.Fatalf("enable: %d %v", n, err)
	}

	// Extend without days: one paid period from each user's own expiry; it turns them on.
	off := true
	if _, err := users.Update(ctx, ids[1], Patch{ClearExpiry: true, Disabled: &off}); err != nil {
		t.Fatal(err)
	}
	exp0 := get(ids[0]).ExpiresAt.Int64
	if n, err := users.Bulk(ctx, ids, BulkExtend, BulkOpt{}); err != nil || n != 2 {
		t.Fatalf("extend: %d %v", n, err)
	}
	if got := get(ids[0]).ExpiresAt.Int64; got != exp0+30*86400 {
		t.Fatalf("extended from the expiry: %d, want %d", got, exp0+30*86400)
	}
	if u := get(ids[1]); u.ExpiresAt.Int64 != now.Unix()+30*86400 || u.Status != "active" {
		t.Fatalf("extended from now and turned on: %d %s", u.ExpiresAt.Int64, u.Status)
	}

	// Reset: a new period now, the period's grants end, the others stay.
	if _, err := st.DB.ExecContext(ctx, "UPDATE users SET used_up = 5, used_down = 7"); err != nil {
		t.Fatal(err)
	}
	u0 := get(ids[0])
	once, _ := GrantTx(ctx, st.Q, u0, GrantSpec{Bytes: 50, Lifetime: LifetimePeriod, Source: SourceAdmin}, now)
	keep, _ := GrantTx(ctx, st.Q, u0, GrantSpec{Bytes: 50, Lifetime: LifetimeUsed, Source: SourceAdmin}, now)
	now = now.Add(time.Hour)
	if n, err := users.Bulk(ctx, ids, BulkReset, BulkOpt{}); err != nil || n != 2 {
		t.Fatalf("reset: %d %v", n, err)
	}
	for _, id := range ids {
		if u := get(id); u.UsedUp+u.UsedDown != 0 || u.PeriodStart != now.Unix() {
			t.Fatalf("user %d after the reset: used %d/%d, period %d", id, u.UsedUp, u.UsedDown, u.PeriodStart)
		}
	}
	left, _ := UserGrantsLeft(ctx, st.Q, ids[0], now)
	if left.Main(ids[0]) != 50 {
		t.Fatalf("grants after the reset: %d left, want the endless %d's 50 (period grant %d ends)", left.Main(ids[0]), keep.ID, once.ID)
	}

	// Delete: the users' slots and their devices' slots are burned.
	dev, err := devs.Bind(ctx, get(ids[0]), DeviceInfo{HWID: "phone-0123456789", App: "Happ"}, false)
	if err != nil {
		t.Fatal(err)
	}
	own := get(ids[1]).SlotID.Int64
	if n, err := users.Bulk(ctx, ids, BulkDelete, BulkOpt{}); err != nil || n != 2 {
		t.Fatalf("delete: %d %v", n, err)
	}
	for _, id := range []int64{dev.ID, own} {
		if s, _ := st.Q.GetSlot(ctx, id); s.State != "burned" || !s.BurnedAt.Valid {
			t.Fatalf("slot %d after the delete: %s", id, s.State)
		}
	}
	if devices, _ := st.Q.ListBoundDevices(ctx, ids[0]); len(devices) != 0 {
		t.Fatalf("bound devices left: %+v", devices)
	}
}
