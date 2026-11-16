package domain

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

// Admit tells what Bind would do with a device, whatever the device and the subscription
// are like, and writes nothing: no device takes a place, and a known one's last visit stays
// as it was.
func TestAdmitTellsWhatBindDoes(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, users, ch := setup(t, &now)
	ctx := context.Background()
	clock := func() time.Time { return now }
	devs := NewDevices(st, NewPool(st, clock), ch, clock)
	if err := settings.Set(ctx, settings.New(st.Q), settings.KeyUnbindRules, UnbindRules{Limit: 2, Days: 7, ReturnHours: 6}); err != nil {
		t.Fatal(err)
	}
	tariffs, _ := st.Q.ListTariffs(ctx)
	u, err := users.Create(ctx, CreateInput{Name: "a", TariffID: tariffs[1].ID}) // Стандарт: 3 devices
	if err != nil {
		t.Fatal(err)
	}
	devices := func() []db.BoundDevice {
		t.Helper()
		list, err := st.Q.ListBoundDevices(ctx, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		return list
	}
	// check admits in for user, sees that nothing was written, then binds it: the two agree.
	check := func(user db.User, in DeviceInfo, requireHWID bool, want string, wantErr error) {
		t.Helper()
		before := devices()
		got, err := devs.Admit(ctx, user, in, requireHWID)
		if got != want || !errors.Is(err, wantErr) {
			t.Fatalf("%+v: Admit %q %v, want %q %v", in, got, err, want, wantErr)
		}
		if after := devices(); !reflect.DeepEqual(after, before) {
			t.Fatalf("%+v: Admit wrote %+v, was %+v", in, after, before)
		}
		if _, err := devs.Bind(ctx, user, in, requireHWID); !errors.Is(err, wantErr) {
			t.Fatalf("%+v: Bind %v, Admit said %v", in, err, wantErr)
		}
	}
	phone := DeviceInfo{HWID: "phone-0123456789", OS: "Android", App: "Happ/3.4", IP: "203.0.113.5"}
	check(u, phone, false, AdmitNew, nil)
	check(u, DeviceInfo{HWID: phone.HWID, App: "Happ/3.5", IP: "203.0.113.9"}, false, AdmitKnown, nil)
	check(u, DeviceInfo{App: "clash-verge/v2"}, true, "", ErrNoHWID)
	check(u, DeviceInfo{App: "clash-verge/v2"}, false, AdmitShared, nil)
	// An id that is no id is none: the shared place, taken by now.
	check(u, DeviceInfo{HWID: "bad id!", App: "sing-box"}, false, AdmitKnown, nil)
	check(u, DeviceInfo{HWID: "laptop-0123456789"}, false, AdmitNew, nil)
	check(u, DeviceInfo{HWID: "tablet-0123456789"}, false, "", ErrDeviceLimit)

	// A device the admin banned, and one its subscriber unbound a while ago.
	admin, err := st.Q.CreateAdmin(ctx, db.CreateAdminParams{Username: "root", PasswordHash: "x", CreatedAt: now.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range devices() {
		switch d.Hwid {
		case phone.HWID:
			if _, err := devs.Ban(ctx, u.ID, d.ID, admin.ID); err != nil {
				t.Fatal(err)
			}
		case "laptop-0123456789":
			if err := devs.Unbind(ctx, u.ID, d.ID, true); err != nil {
				t.Fatal(err)
			}
		}
	}
	check(u, phone, false, "", ErrDeviceBanned)
	var ue *UnboundError
	if got, err := devs.Admit(ctx, u, DeviceInfo{HWID: "laptop-0123456789"}, false); got != "" || !errors.As(err, &ue) || !ue.Until.Equal(now.Add(6*time.Hour).UTC()) {
		t.Fatalf("the laptop unbound: %q %v", got, err)
	}

	// A subscription that cannot connect gets the user's own keys and takes no place.
	off := u
	off.Status = "disabled"
	check(off, DeviceInfo{HWID: "tv-0123456789ab"}, false, AdmitInactive, nil)
	if list := devices(); len(list) != 1 || list[0].Hwid != "" {
		t.Fatalf("only the shared place is left: %+v", list)
	}
}
