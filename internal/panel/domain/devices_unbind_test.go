package domain

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

// The admin's rules: so many unbinds a sliding window, a pause before an unbound device
// may come back, none of it for the admin.
func TestUnbindRules(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, users, ch := setup(t, &now)
	ctx := context.Background()
	clock := func() time.Time { return now }
	devs := NewDevices(st, NewPool(st, clock), ch, clock)
	set := settings.New(st.Q)
	if err := settings.Set(ctx, set, settings.KeyUnbindRules, UnbindRules{Limit: 2, Days: 7, ReturnHours: 6}); err != nil {
		t.Fatal(err)
	}
	tariffs, _ := st.Q.ListTariffs(ctx)
	u, err := users.Create(ctx, CreateInput{Name: "a", TariffID: tariffs[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, "UPDATE users SET device_limit = 10 WHERE id = $1", u.ID); err != nil {
		t.Fatal(err)
	}
	if u, err = st.Q.GetUser(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	bind := func(hwid string) error {
		_, err := devs.Bind(ctx, u, DeviceInfo{HWID: hwid, Model: hwid}, false)
		return err
	}
	unbind := func(hwid string, bySubscriber bool) error {
		list, _ := st.Q.ListBoundDevices(ctx, u.ID)
		for _, d := range list {
			if d.Hwid == hwid {
				return devs.Unbind(ctx, u.ID, d.ID, bySubscriber)
			}
		}
		t.Fatalf("%s is not bound", hwid)
		return nil
	}
	for _, h := range []string{"phone-0123456789", "laptop-0123456789", "tablet-0123456789", "tv-0123456789ab"} {
		if err := bind(h); err != nil {
			t.Fatal(err)
		}
	}

	// Two a week: the third waits until the first leaves the window.
	if err := unbind("phone-0123456789", true); err != nil {
		t.Fatal(err)
	}
	first := now
	now = now.Add(48 * time.Hour)
	if s, _ := devs.UnbindState(ctx, u.ID); s.Left() != 1 || !s.Next.IsZero() {
		t.Fatalf("one of two used: %+v", s)
	}
	if err := unbind("laptop-0123456789", true); err != nil {
		t.Fatal(err)
	}
	if err := unbind("tablet-0123456789", true); !errors.Is(err, ErrUnbindCooldown) {
		t.Fatalf("a third in the week: %v", err)
	}
	if s, _ := devs.UnbindState(ctx, u.ID); s.Left() != 0 || !s.Next.Equal(first.Add(7*24*time.Hour).UTC()) {
		t.Fatalf("the next comes when the first leaves the window: %+v", s)
	}
	// The admin unbinds past the limit, and the device they unbind is not held.
	if err := unbind("tablet-0123456789", false); err != nil {
		t.Fatalf("the admin: %v", err)
	}
	if err := bind("tablet-0123456789"); err != nil {
		t.Fatalf("a device the admin unbound comes back at once: %v", err)
	}

	// The laptop the subscriber unbound stays out for six hours, as a ban that ends.
	var ue *UnboundError
	if err := bind("laptop-0123456789"); !errors.As(err, &ue) || !ue.Until.Equal(now.Add(6*time.Hour).UTC()) {
		t.Fatalf("the unbound laptop comes back at once: %v", err)
	}
	if banned, _ := devs.Banned(ctx, u.ID, "laptop-0123456789"); banned {
		t.Fatal("the pause is no ban of the admin's: without binding the laptop gets its keys")
	}
	bans, _ := st.Q.ListDeviceBans(ctx, db.ListDeviceBansParams{UserID: u.ID, Until: sql.NullInt64{Int64: now.Unix(), Valid: true}})
	if len(bans) != 1 || !bans[0].Until.Valid || bans[0].Label != "laptop-0123456789" {
		t.Fatalf("the admin sees the pause among the bans: %+v", bans)
	}
	// The admin may let it in earlier.
	if _, err := devs.Unban(ctx, u.ID, bans[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := bind("laptop-0123456789"); err != nil {
		t.Fatalf("let in by the admin: %v", err)
	}
	// The phone's pause ended by itself; Prune clears it.
	if err := bind("phone-0123456789"); err != nil {
		t.Fatalf("the pause is over: %v", err)
	}
	if err := devs.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	if bans, _ := st.Q.ListDeviceBans(ctx, db.ListDeviceBansParams{UserID: u.ID, Until: sql.NullInt64{Int64: 0, Valid: true}}); len(bans) != 0 {
		t.Fatalf("ended pauses stay: %+v", bans)
	}

	// An admin's ban stays for good: a pause does not shorten it.
	b, err := devs.Ban(ctx, u.ID, mustDevice(t, st.Q, u.ID, "tv-0123456789ab"), 0)
	if err != nil || b.Until.Valid {
		t.Fatalf("ban: %+v %v", b, err)
	}
	if err := bind("tv-0123456789ab"); !errors.Is(err, ErrDeviceBanned) {
		t.Fatalf("a banned device: %v", err)
	}

	// No limit: as many as they like, and without a pause they come back at once.
	if err := settings.Set(ctx, set, settings.KeyUnbindRules, UnbindRules{Limit: 0, Days: 1, ReturnHours: 0}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := unbind("phone-0123456789", true); err != nil {
			t.Fatalf("without a limit: %v", err)
		}
		if err := bind("phone-0123456789"); err != nil {
			t.Fatalf("without a pause: %v", err)
		}
	}
	if s, _ := devs.UnbindState(ctx, u.ID); s.Left() != -1 || !s.Next.IsZero() {
		t.Fatalf("no limit: %+v", s)
	}
}

func mustDevice(t *testing.T, q *db.Queries, userID int64, hwid string) int64 {
	t.Helper()
	list, _ := q.ListBoundDevices(context.Background(), userID)
	for _, d := range list {
		if d.Hwid == hwid {
			return d.ID
		}
	}
	t.Fatalf("%s is not bound", hwid)
	return 0
}

func TestUnbindRulesValidate(t *testing.T) {
	for _, r := range []UnbindRules{{Limit: -1, Days: 1}, {Limit: 1, Days: 0}, {Limit: 1, Days: 366}, {Limit: 101, Days: 1}, {Limit: 1, Days: 1, ReturnHours: 721}} {
		if r.Validate() == nil {
			t.Errorf("%+v passes", r)
		}
	}
	if err := (UnbindRules{Limit: 0, Days: 365, ReturnHours: 720}).Validate(); err != nil {
		t.Errorf("the edges: %v", err)
	}
	if DefaultUnbindRules.Validate() != nil {
		t.Error("the default does not pass")
	}
}
