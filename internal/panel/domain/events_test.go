package domain

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/events"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

// feedOf is every event written so far, in id order.
func feedOf(t *testing.T, st *store.Store) []db.AddonEvent {
	t.Helper()
	evs, err := st.Q.ListAddonEvents(context.Background(), db.ListAddonEventsParams{Types: []string{}, Lim: 500})
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func typesOf(evs []db.AddonEvent) []string {
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.Type)
	}
	return out
}

// A user the admin makes is told once, with where it came from; one rolled back is not told,
// and an import, a bulk move of people, tells nothing.
func TestUserCreatedEvent(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, users, _ := setup(t, &now)
	hub := events.NewHub()
	users.SetEvents(hub)
	ctx := context.Background()
	tariffs, _ := st.Q.ListTariffs(ctx)
	woken := hub.Wait()
	u, err := users.Create(ctx, CreateInput{Name: "a", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-woken:
	default:
		t.Fatal("a waiting reader is not woken")
	}
	evs := feedOf(t, st)
	if len(evs) != 1 || evs[0].Type != events.UserCreated || evs[0].UserID.Int64 != u.ID || evs[0].TgID.Valid {
		t.Fatalf("events: %+v", evs)
	}
	var got events.User
	if err := json.Unmarshal([]byte(evs[0].Data), &got); err != nil || got.Source != UserFromAdmin || got.TariffID != tariffs[1].ID {
		t.Fatalf("data %s: %+v %v", evs[0].Data, got, err)
	}

	// A user made on a transaction that fails is not told of, and does not exist.
	boom := errors.New("boom")
	err = st.Tx(ctx, func(q *db.Queries) error {
		if _, err := users.CreateOn(ctx, q, CreateInput{Name: "gone", TariffID: tariffs[1].ID, Source: UserFromBot, TgID: 7}, Patch{}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("the transaction: %v", err)
	}
	if n := len(feedOf(t, st)); n != 1 {
		t.Fatalf("%d events after a rolled back user", n)
	}

	// The bot's own and an import.
	if err := st.Tx(ctx, func(q *db.Queries) error {
		if _, err := users.CreateOn(ctx, q, CreateInput{Name: "buyer", TariffID: tariffs[1].ID, Source: UserFromBot, TgID: 7}, Patch{}); err != nil {
			return err
		}
		_, err := users.CreateOn(ctx, q, CreateInput{Name: "moved", TariffID: tariffs[1].ID, Source: UserFromImport}, Patch{})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	evs = feedOf(t, st)
	if len(evs) != 2 || evs[1].TgID.Int64 != 7 {
		t.Fatalf("events after the bot's user and an import: %+v", evs)
	}
	if err := json.Unmarshal([]byte(evs[1].Data), &got); err != nil || got.Source != UserFromBot {
		t.Fatalf("data %s: %+v %v", evs[1].Data, got, err)
	}
}

// Devices taking and losing a place are told once each, in the transaction of the change, with
// how they were unbound, and never with their id at the app.
func TestDeviceEvents(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, users, ch := setup(t, &now)
	ctx := context.Background()
	clock := func() time.Time { return now }
	devs := NewDevices(st, NewPool(st, clock), ch, clock)
	hub := events.NewHub()
	devs.SetEvents(hub)
	users.SetEvents(hub)
	tariffs, _ := st.Q.ListTariffs(ctx)
	u, err := users.Create(ctx, CreateInput{Name: "a", TariffID: tariffs[1].ID}) // 3 devices
	if err != nil {
		t.Fatal(err)
	}
	// The term must outlast the idle wait below.
	if _, err := users.Update(ctx, u.ID, Patch{ClearExpiry: true}); err != nil {
		t.Fatal(err)
	}
	if u, err = users.Get(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	after := len(feedOf(t, st)) // the user.created

	woken := hub.Wait()
	phone := DeviceInfo{HWID: "phone-0123456789", Model: "Pixel 9", App: "Happ"}
	if _, err := devs.Bind(ctx, u, phone, false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-woken:
	default:
		t.Fatal("a waiting reader is not woken")
	}
	// The same device again, and a device the limit turns away: nothing to tell.
	if _, err := devs.Bind(ctx, u, phone, false); err != nil {
		t.Fatal(err)
	}
	if _, err := devs.Bind(ctx, u, DeviceInfo{App: "clash-verge"}, false); err != nil { // the shared place
		t.Fatal(err)
	}
	if _, err := devs.Bind(ctx, u, DeviceInfo{HWID: "laptop-0123456789", Model: "Laptop"}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := devs.Bind(ctx, u, DeviceInfo{HWID: "tablet-0123456789"}, false); !errors.Is(err, ErrDeviceLimit) {
		t.Fatalf("a fourth device: %v", err)
	}
	evs := feedOf(t, st)[after:]
	if !slices.Equal(typesOf(evs), []string{events.DeviceBound, events.DeviceBound, events.DeviceBound}) {
		t.Fatalf("events of binding: %v", typesOf(evs))
	}
	var first, shared events.Device
	if err := json.Unmarshal([]byte(evs[0].Data), &first); err != nil || first.Label != "Pixel 9" || first.Shared || first.DeviceID == 0 || first.By != "" {
		t.Fatalf("the phone: %s %v", evs[0].Data, err)
	}
	if err := json.Unmarshal([]byte(evs[1].Data), &shared); err != nil || !shared.Shared {
		t.Fatalf("the shared place: %s %v", evs[1].Data, err)
	}
	for _, e := range evs {
		if e.UserID.Int64 != u.ID || e.TgID.Valid {
			t.Fatalf("who: %+v", e)
		}
		if e.Data == "" || containsHWID(e.Data) {
			t.Fatalf("the device's id leaks: %s", e.Data)
		}
	}

	// The subscriber unbinds the phone; the admin the laptop.
	list, _ := st.Q.ListBoundDevices(ctx, u.ID)
	byModel := map[string]db.BoundDevice{}
	for _, d := range list {
		byModel[d.Model] = d
	}
	if err := devs.Unbind(ctx, u.ID, byModel["Pixel 9"].ID, true); err != nil {
		t.Fatal(err)
	}
	if err := devs.Unbind(ctx, u.ID, byModel["Laptop"].ID, false); err != nil {
		t.Fatal(err)
	}
	// An unbind of a device that is not the user's changes nothing and tells nothing.
	before := len(feedOf(t, st))
	if err := devs.Unbind(ctx, u.ID, byModel["Laptop"].ID, false); err == nil {
		t.Fatal("a device unbound twice")
	}
	if n := len(feedOf(t, st)); n != before {
		t.Fatalf("a refused unbind told %d events", n-before)
	}
	unbound := func(evs []db.AddonEvent) (by []string) {
		for _, e := range evs {
			if e.Type != events.DeviceUnbound {
				continue
			}
			var d events.Device
			if err := json.Unmarshal([]byte(e.Data), &d); err != nil {
				t.Fatal(err)
			}
			by = append(by, d.By)
		}
		return by
	}
	if got := unbound(feedOf(t, st)); !slices.Equal(got, []string{events.UnboundBySubscriber, events.UnboundByAdmin}) {
		t.Fatalf("unbound by %v", got)
	}

	// A ban, an idle device and a new link.
	if _, err := devs.Bind(ctx, u, DeviceInfo{HWID: "tv-0123456789ab", Model: "TV"}, false); err != nil {
		t.Fatal(err)
	}
	list, _ = st.Q.ListBoundDevices(ctx, u.ID)
	for _, d := range list {
		if d.Model == "TV" {
			if _, err := devs.Ban(ctx, u.ID, d.ID, 0); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := devs.Bind(ctx, u, DeviceInfo{HWID: "old-0123456789abc", Model: "Old"}, false); err != nil {
		t.Fatal(err)
	}
	now = now.Add(DeviceIdle + 24*time.Hour)
	if n, err := devs.ForgetIdle(ctx); err != nil || n == 0 {
		t.Fatalf("idle: %d %v", n, err)
	}
	if _, err := devs.Bind(ctx, u, DeviceInfo{HWID: "new-0123456789abc", Model: "New"}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := users.Reissue(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	got := unbound(feedOf(t, st))
	if len(got) < 5 || got[2] != events.UnboundByBan || got[3] != events.UnboundIdle || got[len(got)-1] != events.UnboundReissue {
		t.Fatalf("unbound by %v", got)
	}
	if left, _ := st.Q.ListBoundDevices(ctx, u.ID); len(left) != 0 {
		t.Fatalf("devices after a new link: %+v", left)
	}
	// Each device is told bound once and unbound once: ids are never reused.
	seen := map[string]int{}
	for _, e := range feedOf(t, st) {
		if e.DedupeKey.Valid {
			seen[e.DedupeKey.String]++
		}
	}
	for k, n := range seen {
		if n != 1 {
			t.Fatalf("%s told %d times", k, n)
		}
	}
}

func containsHWID(data string) bool {
	for _, h := range []string{"phone-0123456789", "laptop-0123456789", "tv-0123456789ab", "hwid"} {
		if strings.Contains(data, h) {
			return true
		}
	}
	return false
}
