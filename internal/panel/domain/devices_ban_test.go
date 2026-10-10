package domain

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/store/db"
)

// A device gets a name of its own, from the admin or the subscriber; another user's device
// cannot be renamed, and the name is checked.
func TestRenameDevice(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, users, ch := setup(t, &now)
	ctx := context.Background()
	clock := func() time.Time { return now }
	devs := NewDevices(st, NewPool(st, clock), ch, clock)
	tariffs, _ := st.Q.ListTariffs(ctx)
	a, _ := users.Create(ctx, CreateInput{Name: "a", TariffID: tariffs[1].ID})
	b, _ := users.Create(ctx, CreateInput{Name: "b", TariffID: tariffs[1].ID})
	if _, err := devs.Bind(ctx, a, DeviceInfo{HWID: "phone-0123456789", Model: "Pixel 9"}, false); err != nil {
		t.Fatal(err)
	}
	list, _ := st.Q.ListBoundDevices(ctx, a.ID)
	dev := list[0]
	if DeviceLabel(dev) != "Pixel 9" {
		t.Fatalf("label before a name: %q", DeviceLabel(dev))
	}
	name, err := devs.Rename(ctx, a.ID, dev.ID, "  <b>Мамин</b> телефон ")
	if err != nil || name != "<b>Мамин</b> телефон" {
		t.Fatalf("rename: %q %v", name, err)
	}
	got, _ := st.Q.GetBoundDeviceByID(ctx, db.GetBoundDeviceByIDParams{ID: dev.ID, UserID: a.ID})
	if got.Name != name || DeviceLabel(got) != name || got.SlotID != dev.SlotID {
		t.Fatalf("renamed: %+v", got)
	}
	// The next fetch reports the model again: the name stays.
	if _, err := devs.Bind(ctx, a, DeviceInfo{HWID: "phone-0123456789", Model: "Pixel 10"}, false); err != nil {
		t.Fatal(err)
	}
	if got, _ = st.Q.GetBoundDeviceByID(ctx, db.GetBoundDeviceByIDParams{ID: dev.ID, UserID: a.ID}); got.Name != name || got.Model != "Pixel 10" {
		t.Fatalf("a fetch after the rename: %+v", got)
	}
	if _, err := devs.Rename(ctx, b.ID, dev.ID, "mine"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another user's device: %v", err)
	}
	if _, err := devs.Rename(ctx, a.ID, dev.ID, strings.Repeat("x", 41)); codeOf(err) != "name:"+CodeDeviceNameLong {
		t.Fatalf("too long: %v", err)
	}
	if _, err := devs.Rename(ctx, a.ID, dev.ID, "a\nb"); codeOf(err) != "name:"+CodeNameChars {
		t.Fatalf("a line break: %v", err)
	}
	if name, err := devs.Rename(ctx, a.ID, dev.ID, ""); err != nil || name != "" {
		t.Fatalf("back to the app's name: %q %v", name, err)
	}
	if got, _ = st.Q.GetBoundDeviceByID(ctx, db.GetBoundDeviceByIDParams{ID: dev.ID, UserID: a.ID}); DeviceLabel(got) != "Pixel 10" {
		t.Fatalf("label after clearing: %q", DeviceLabel(got))
	}
}

// A banned device is unbound, its keys burn, and it cannot take a place of the same user
// again until unbanned; the user's other devices and other users are not touched.
func TestBanDevice(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, users, ch := setup(t, &now)
	ctx := context.Background()
	clock := func() time.Time { return now }
	devs := NewDevices(st, NewPool(st, clock), ch, clock)
	tariffs, _ := st.Q.ListTariffs(ctx)
	a, _ := users.Create(ctx, CreateInput{Name: "a", TariffID: tariffs[1].ID})
	b, _ := users.Create(ctx, CreateInput{Name: "b", TariffID: tariffs[1].ID})
	admin, err := st.Q.CreateAdmin(ctx, db.CreateAdminParams{Username: "root", PasswordHash: "x", CreatedAt: now.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	phone := DeviceInfo{HWID: "phone-0123456789", Model: "Pixel 9", OS: "Android"}
	slot, err := devs.Bind(ctx, a, phone, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := devs.Bind(ctx, a, DeviceInfo{App: "clash-verge/v2"}, false); err != nil {
		t.Fatal(err)
	}
	list, _ := st.Q.ListBoundDevices(ctx, a.ID)
	var phoneDev, shared db.BoundDevice
	for _, d := range list {
		if d.Hwid == "" {
			shared = d
		} else {
			phoneDev = d
		}
	}
	if _, err := devs.Rename(ctx, a.ID, phoneDev.ID, "Телефон"); err != nil {
		t.Fatal(err)
	}

	// Neither another user's device nor the shared place of apps without an id.
	if _, err := devs.Ban(ctx, b.ID, phoneDev.ID, admin.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another user's device: %v", err)
	}
	if _, err := devs.Ban(ctx, a.ID, shared.ID, admin.ID); !errors.Is(err, ErrBanShared) {
		t.Fatalf("the shared place: %v", err)
	}
	before := ch.policies
	ban, err := devs.Ban(ctx, a.ID, phoneDev.ID, admin.ID)
	if err != nil || ban.Hwid != phone.HWID || ban.Label != "Телефон" || ban.AdminID.Int64 != admin.ID || ban.BannedAt != now.Unix() {
		t.Fatalf("ban: %+v %v", ban, err)
	}
	if ch.policies == before {
		t.Fatal("the nodes must hear of the burnt keys")
	}
	if s, _ := st.Q.GetSlot(ctx, slot.ID); s.State != "burned" {
		t.Fatalf("the banned device's keys must burn: %s", s.State)
	}
	if n, _ := st.Q.CountBoundDevices(ctx, a.ID); n != 1 {
		t.Fatalf("the banned device keeps a place: %d", n)
	}
	if _, err := devs.Bind(ctx, a, phone, false); !errors.Is(err, ErrDeviceBanned) {
		t.Fatalf("a banned device binds again: %v", err)
	}
	if n, _ := st.Q.CountBoundDevices(ctx, a.ID); n != 1 {
		t.Fatalf("a refused fetch took a place: %d", n)
	}
	if banned, _ := devs.Banned(ctx, a.ID, phone.HWID); !banned {
		t.Fatal("Banned")
	}
	// Per user: the same device binds to another subscription.
	if _, err := devs.Bind(ctx, b, phone, false); err != nil {
		t.Fatalf("the device on another user's subscription: %v", err)
	}
	// Even when the user is off: a banned device is not seated with the user's keys either.
	off := true
	if _, err := users.Update(ctx, a.ID, Patch{Disabled: &off}); err != nil {
		t.Fatal(err)
	}
	a, _ = users.Get(ctx, a.ID)
	if _, err := devs.Bind(ctx, a, phone, false); !errors.Is(err, ErrDeviceBanned) {
		t.Fatalf("banned while the user is off: %v", err)
	}
	on := false
	if _, err := users.Update(ctx, a.ID, Patch{Disabled: &on}); err != nil {
		t.Fatal(err)
	}
	a, _ = users.Get(ctx, a.ID)
	rows, _ := st.Q.ListDeviceBans(ctx, db.ListDeviceBansParams{UserID: a.ID})
	if len(rows) != 1 || rows[0].AdminName != "root" || rows[0].Label != "Телефон" {
		t.Fatalf("bans: %+v", rows)
	}
	// A new link does not lift the ban.
	if _, err := users.Reissue(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if rows, _ := st.Q.ListDeviceBans(ctx, db.ListDeviceBansParams{UserID: a.ID}); len(rows) != 1 {
		t.Fatal("a reissue lifted the ban")
	}

	if _, err := devs.Unban(ctx, b.ID, ban.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unban through another user: %v", err)
	}
	if _, err := devs.Unban(ctx, a.ID, ban.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := devs.Unban(ctx, a.ID, ban.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a second unban: %v", err)
	}
	if _, err := devs.Bind(ctx, a, phone, false); err != nil {
		t.Fatalf("an unbanned device binds again: %v", err)
	}
	if banned, _ := devs.Banned(ctx, a.ID, phone.HWID); banned {
		t.Fatal("still banned")
	}
}
