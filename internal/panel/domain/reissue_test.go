package domain

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

// The subscriber's own new link: one a day by default, the count survives in the database,
// the old link stops working, and the journal says the subscriber did it.
func TestReissueByOwnerWithinTheRules(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, users, ch := setup(t, &now)
	ctx := context.Background()
	tariffs, _ := st.Q.ListTariffs(ctx)
	u, err := users.Create(ctx, CreateInput{Name: "a", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}

	first, err := users.ReissueByOwner(ctx, u.ID, "bot")
	if err != nil {
		t.Fatal(err)
	}
	if first.SubToken == u.SubToken || first.SlotID == u.SlotID {
		t.Fatalf("the link and the keys stay: %+v", first)
	}
	if _, err := st.Q.GetUserBySubToken(ctx, u.SubToken); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("the old link still opens the subscription: %v", err)
	}
	if ch.policies == 0 {
		t.Fatal("the nodes are not told")
	}

	// The default: once a day.
	if _, err := users.ReissueByOwner(ctx, u.ID, "bot"); !errors.Is(err, ErrReissueCooldown) {
		t.Fatalf("the second one the same day: %v", err)
	}
	stt, err := users.ReissueState(ctx, u.ID)
	if err != nil || stt.Left() != 0 || !stt.Next.Equal(now.Add(24*time.Hour).UTC()) {
		t.Fatalf("state: %+v %v", stt, err)
	}
	now = now.Add(24*time.Hour - time.Second)
	if _, err := users.ReissueByOwner(ctx, u.ID, "bot"); !errors.Is(err, ErrReissueCooldown) {
		t.Fatalf("a second before the day is over: %v", err)
	}
	now = now.Add(time.Second)
	second, err := users.ReissueByOwner(ctx, u.ID, "miniapp")
	if err != nil {
		t.Fatalf("after the day: %v", err)
	}
	if second.SubToken == first.SubToken {
		t.Fatal("the link did not change")
	}

	// The admin is not held by the rules.
	if _, err := users.Reissue(ctx, u.ID); err != nil {
		t.Fatalf("the admin's reissue: %v", err)
	}

	// The journal: the subscriber, not an admin, and where from.
	var n int
	if err := st.DB.QueryRowContext(ctx, `SELECT count(*) FROM audit_log WHERE action = 'user.reissue' AND admin_id IS NULL
		AND details::jsonb->>'actor' = 'subscriber' AND target_id = $1`, fmt.Sprint(u.ID)).Scan(&n); err != nil || n != 2 {
		t.Fatalf("audit entries of the subscriber: %d %v", n, err)
	}
	var via string
	if err := st.DB.QueryRowContext(ctx, `SELECT details::jsonb->>'via' FROM audit_log WHERE action = 'user.reissue' ORDER BY id DESC LIMIT 1`).Scan(&via); err != nil || via != "miniapp" {
		t.Fatalf("via: %q %v", via, err)
	}
}

func TestReissueRulesOffAndWindows(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, users, _ := setup(t, &now)
	ctx := context.Background()
	set := settings.New(st.Q)
	tariffs, _ := st.Q.ListTariffs(ctx)
	u, err := users.Create(ctx, CreateInput{Name: "a", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}

	if err := settings.Set(ctx, set, settings.KeyReissueRules, ReissueRules{Off: true, Limit: 1, Days: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := users.ReissueByOwner(ctx, u.ID, "bot"); !errors.Is(err, ErrReissueOff) {
		t.Fatalf("off: %v", err)
	}
	if stt, _ := users.ReissueState(ctx, u.ID); stt.Left() != 0 {
		t.Fatalf("off leaves %d", stt.Left())
	}

	// Two a week, counted in a sliding window.
	if err := settings.Set(ctx, set, settings.KeyReissueRules, ReissueRules{Limit: 2, Days: 7}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := users.ReissueByOwner(ctx, u.ID, "bot"); err != nil {
			t.Fatalf("reissue %d: %v", i, err)
		}
		now = now.Add(time.Hour)
	}
	if _, err := users.ReissueByOwner(ctx, u.ID, "bot"); !errors.Is(err, ErrReissueCooldown) {
		t.Fatalf("the third in a week: %v", err)
	}
	now = now.Add(7 * 24 * time.Hour)
	if _, err := users.ReissueByOwner(ctx, u.ID, "bot"); err != nil {
		t.Fatalf("once the window has moved on: %v", err)
	}

	// No limit: as often as they like.
	if err := settings.Set(ctx, set, settings.KeyReissueRules, ReissueRules{Limit: 0, Days: 1}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if _, err := users.ReissueByOwner(ctx, u.ID, "bot"); err != nil {
			t.Fatalf("unlimited, %d: %v", i, err)
		}
	}
	if stt, _ := users.ReissueState(ctx, u.ID); stt.Left() != -1 {
		t.Fatalf("unlimited leaves %d", stt.Left())
	}

	// A user that is not there.
	if _, err := users.ReissueByOwner(ctx, 99999, "bot"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a missing user: %v", err)
	}
	// Rules stored broken fall back to the default.
	if err := settings.Set(ctx, set, settings.KeyReissueRules, ReissueRules{Limit: 5000, Days: 0}); err != nil {
		t.Fatal(err)
	}
	if r, err := LoadReissueRules(ctx, set); err != nil || r != DefaultReissueRules {
		t.Fatalf("broken rules: %+v %v", r, err)
	}
}

// The reissues of a user are dropped with the user and by the daily cleanup once no window
// reaches them.
func TestReissuesArePruned(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, users, ch := setup(t, &now)
	ctx := context.Background()
	clock := func() time.Time { return now }
	devs := NewDevices(st, NewPool(st, clock), ch, clock)
	tariffs, _ := st.Q.ListTariffs(ctx)
	u, err := users.Create(ctx, CreateInput{Name: "a", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := users.ReissueByOwner(ctx, u.ID, "bot"); err != nil {
		t.Fatal(err)
	}
	count := func() (n int) {
		_ = st.DB.QueryRowContext(ctx, "SELECT count(*) FROM link_reissues").Scan(&n)
		return n
	}
	if err := devs.Prune(ctx); err != nil || count() != 1 {
		t.Fatalf("a fresh one is kept: %d %v", count(), err)
	}
	now = now.Add(unbindKeep + time.Hour)
	if err := devs.Prune(ctx); err != nil || count() != 0 {
		t.Fatalf("an old one is dropped: %d %v", count(), err)
	}
}

// "bound" is today's rule: the limit is the number of places. "online" lets any number of
// devices bind (the nodes count the ones online at once), up to the ceiling that protects
// the pool of slots.
func TestDeviceModeOnlineDoesNotCapBinding(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	devs, users, newUser := newDevices(t, &now)
	ctx := context.Background()
	bound, online := newUser("bound"), newUser("online")
	mode := DeviceModeOnline
	if _, err := users.Update(ctx, online, Patch{DeviceMode: &mode}); err != nil {
		t.Fatal(err)
	}
	two := int64(2)
	for _, id := range []int64{bound, online} {
		if _, err := users.Update(ctx, id, Patch{DeviceLimit: &two}); err != nil {
			t.Fatal(err)
		}
	}
	try := func(id int64, n int) error {
		u, _ := users.Get(ctx, id)
		_, err := devs.Bind(ctx, u, DeviceInfo{HWID: fmt.Sprintf("dev-%d-%08d", id, n)}, false)
		return err
	}
	for i := 0; i < 2; i++ {
		if err := try(bound, i); err != nil {
			t.Fatalf("bound %d: %v", i, err)
		}
	}
	if err := try(bound, 2); !errors.Is(err, ErrDeviceLimit) {
		t.Fatalf("the third bound device: %v", err)
	}
	for i := 0; i < 10; i++ {
		if err := try(online, i); err != nil {
			t.Fatalf("online %d: %v", i, err)
		}
	}
	if n, _ := devs.st.Q.CountBoundDevices(ctx, online); n != 10 {
		t.Fatalf("bound devices of the online user: %d", n)
	}
	// The limit itself stays for the nodes.
	if u, _ := users.Get(ctx, online); u.DeviceLimit.Int64 != 2 || u.DeviceMode != DeviceModeOnline {
		t.Fatalf("the user: %+v", u)
	}
	// The ceiling holds for the online mode too.
	for i := 10; i < MaxDevices; i++ {
		if err := try(online, i); err != nil {
			t.Fatalf("online %d: %v", i, err)
		}
	}
	if err := try(online, MaxDevices); !errors.Is(err, ErrDeviceLimit) {
		t.Fatalf("past the ceiling: %v", err)
	}
}

// A user takes the tariff's mode with its other limits: on create, on a tariff applied
// later, on a renewal; a mode named in the patch wins over the tariff's.
func TestUserTakesTheTariffsDeviceMode(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, users, _ := setup(t, &now)
	ctx := context.Background()
	tariffs, _ := st.Q.ListTariffs(ctx)
	if tariffs[1].DeviceMode != DeviceModeBound {
		t.Fatalf("a tariff starts as bound: %q", tariffs[1].DeviceMode)
	}
	if err := st.Q.SetTariffDeviceMode(ctx, db.SetTariffDeviceModeParams{DeviceMode: DeviceModeOnline, ID: tariffs[2].ID}); err != nil {
		t.Fatal(err)
	}

	u, err := users.Create(ctx, CreateInput{Name: "plain", TariffID: tariffs[1].ID})
	if err != nil || u.DeviceMode != DeviceModeBound {
		t.Fatalf("bound tariff: %+v %v", u, err)
	}
	u, err = users.Create(ctx, CreateInput{Name: "online", TariffID: tariffs[2].ID})
	if err != nil || u.DeviceMode != DeviceModeOnline {
		t.Fatalf("online tariff: %q %v", u.DeviceMode, err)
	}
	if got, _ := users.Get(ctx, u.ID); got.DeviceMode != DeviceModeOnline {
		t.Fatalf("not stored: %q", got.DeviceMode)
	}

	// Another tariff applied to the user.
	got, err := users.Update(ctx, u.ID, Patch{TariffID: &tariffs[1].ID})
	if err != nil || got.DeviceMode != DeviceModeBound {
		t.Fatalf("back to a bound tariff: %q %v", got.DeviceMode, err)
	}
	got, err = users.Update(ctx, u.ID, Patch{TariffID: &tariffs[2].ID})
	if err != nil || got.DeviceMode != DeviceModeOnline {
		t.Fatalf("to the online tariff: %q %v", got.DeviceMode, err)
	}
	// The admin's own choice, with the tariff or after it.
	bound := DeviceModeBound
	got, err = users.Update(ctx, u.ID, Patch{DeviceMode: &bound})
	if err != nil || got.DeviceMode != DeviceModeBound {
		t.Fatalf("the admin's choice: %q %v", got.DeviceMode, err)
	}
	junk := "everywhere"
	if _, err := users.Update(ctx, u.ID, Patch{DeviceMode: &junk}); err == nil {
		t.Fatal("a mode that does not exist is taken")
	}

	// A renewal takes the tariff's limits, the mode among them.
	err = st.Tx(ctx, func(q *db.Queries) error {
		var err error
		got, _, err = users.Purchase(ctx, q, PurchaseInput{UserID: u.ID, TariffID: tariffs[2].ID})
		return err
	})
	if err != nil || got.DeviceMode != DeviceModeOnline {
		t.Fatalf("renewal: %q %v", got.DeviceMode, err)
	}
	// And a user a purchase creates.
	err = st.Tx(ctx, func(q *db.Queries) error {
		var err error
		got, _, err = users.Purchase(ctx, q, PurchaseInput{TariffID: tariffs[2].ID, Name: "bought"})
		return err
	})
	if err != nil || got.DeviceMode != DeviceModeOnline {
		t.Fatalf("a new purchase: %q %v", got.DeviceMode, err)
	}
}
