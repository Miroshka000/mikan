package domain

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDeviceBinding(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, users, ch := setup(t, &now)
	ctx := context.Background()
	clock := func() time.Time { return now }
	devs := NewDevices(st, NewPool(st, clock), ch, clock)
	tariffs, _ := st.Q.ListTariffs(ctx)
	u, err := users.Create(ctx, CreateInput{Name: "a", TariffID: tariffs[1].ID}) // Стандарт: 3 devices
	if err != nil {
		t.Fatal(err)
	}
	if !u.DeviceLimit.Valid || u.DeviceLimit.Int64 != 3 {
		t.Fatalf("tariff device limit: %v", u.DeviceLimit)
	}
	phone := DeviceInfo{HWID: "phone-0123456789", OS: "Android", OSVersion: "15", Model: "Pixel 9", App: "Happ/3.4", IP: "203.0.113.5"}

	s1, err := devs.Bind(ctx, u, phone, false)
	if err != nil || s1.ID == u.SlotID.Int64 {
		t.Fatalf("a device with an id gets keys of its own: slot %d (user's %d) %v", s1.ID, u.SlotID.Int64, err)
	}
	again, err := devs.Bind(ctx, u, DeviceInfo{HWID: phone.HWID, OS: "Android", App: "Happ/3.5", IP: "203.0.113.9"}, false)
	if err != nil || again.ID != s1.ID {
		t.Fatalf("the same device keeps its keys: %d vs %d %v", again.ID, s1.ID, err)
	}
	// Apps without an id share the user's keys: one place for all of them.
	shared, err := devs.Bind(ctx, u, DeviceInfo{App: "clash-verge/v2"}, false)
	if err != nil || shared.ID != u.SlotID.Int64 {
		t.Fatalf("shared place: %d %v", shared.ID, err)
	}
	if _, err := devs.Bind(ctx, u, DeviceInfo{HWID: "bad id!", App: "sing-box"}, false); err != nil {
		t.Fatalf("an invalid id counts as none: %v", err)
	}
	laptop := DeviceInfo{HWID: "laptop-0123456789", OS: "Windows", App: "Koala Clash"}
	if _, err := devs.Bind(ctx, u, laptop, false); err != nil {
		t.Fatalf("third place: %v", err)
	}
	if _, err := devs.Bind(ctx, u, DeviceInfo{HWID: "tablet-0123456789"}, false); !errors.Is(err, ErrDeviceLimit) {
		t.Fatalf("a fourth device: %v", err)
	}
	if _, err := devs.Bind(ctx, u, DeviceInfo{App: "x"}, true); !errors.Is(err, ErrNoHWID) {
		t.Fatalf("an app without an id when one is required: %v", err)
	}
	list, _ := st.Q.ListBoundDevices(ctx, u.ID)
	if len(list) != 3 || list[0].Model != "Pixel 9" || list[0].App != "Happ/3.5" || list[0].LastIp != "203.0.113.9" {
		t.Fatalf("devices: %+v", list)
	}

	// The subscriber unbinds the phone: its keys burn, the place frees, once a day.
	if err := devs.Unbind(ctx, u.ID, list[0].ID, true); err != nil {
		t.Fatal(err)
	}
	if s, _ := st.Q.GetSlot(ctx, s1.ID); s.State != "burned" {
		t.Fatalf("the unbound device's keys must burn: %s", s.State)
	}
	if _, err := devs.Bind(ctx, u, DeviceInfo{HWID: "tablet-0123456789"}, false); err != nil {
		t.Fatalf("the freed place: %v", err)
	}
	list, _ = st.Q.ListBoundDevices(ctx, u.ID)
	if err := devs.Unbind(ctx, u.ID, list[0].ID, true); !errors.Is(err, ErrUnbindCooldown) {
		t.Fatalf("a second unbind the same day: %v", err)
	}
	u, _ = st.Q.GetUser(ctx, u.ID)
	if state, _ := devs.UnbindState(ctx, u.ID); !state.Next.Equal(now.Add(24*time.Hour).UTC()) || state.Left() != 0 {
		t.Fatalf("next unbind under the default rules: %+v", state)
	}
	// The admin is not limited. Unbinding the shared place gives the user new own keys.
	var sharedDev int64
	for _, d := range list {
		if d.Hwid == "" {
			sharedDev = d.ID
		}
	}
	if err := devs.Unbind(ctx, u.ID, sharedDev, false); err != nil {
		t.Fatal(err)
	}
	after, _ := st.Q.GetUser(ctx, u.ID)
	if after.SlotID.Int64 == u.SlotID.Int64 || after.SubToken != u.SubToken {
		t.Fatalf("shared place unbound: new own keys, same link: %v→%v", u.SlotID, after.SlotID)
	}
	if s, _ := st.Q.GetSlot(ctx, u.SlotID.Int64); s.State != "burned" {
		t.Fatalf("the old own keys burn: %s", s.State)
	}
	if err := devs.Unbind(ctx, u.ID, 999, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown device: %v", err)
	}

	// A new link: every device registers again, their keys burn.
	list, _ = st.Q.ListBoundDevices(ctx, u.ID)
	if _, err := users.Reissue(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	for _, d := range list {
		if s, _ := st.Q.GetSlot(ctx, d.SlotID); d.Hwid != "" && s.State != "burned" {
			t.Fatalf("reissue must burn %s's keys: %s", d.Hwid, s.State)
		}
	}
	if n, _ := st.Q.CountBoundDevices(ctx, u.ID); n != 0 {
		t.Fatalf("devices after reissue: %d", n)
	}
}
