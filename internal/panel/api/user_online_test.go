package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/store/storetest"
)

// Where a user is online is told apart by node and by the keys online there (the user's
// own, a bound device's); other users' slots and empty entries stay out.
func TestUserOnlineByNodeAndDevice(t *testing.T) {
	ctx := context.Background()
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Unix(1_800_000_000, 0)
	clock := func() time.Time { return now }
	if err := domain.Seed(ctx, st, now); err != nil {
		t.Fatal(err)
	}
	tariffs, _ := st.Q.ListTariffs(ctx)
	pool := domain.NewPool(st, clock)
	users := domain.NewUsers(st, pool, noChanges{}, clock)
	u, err := users.Create(ctx, domain.CreateInput{Name: "a", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	other, err := users.Create(ctx, domain.CreateInput{Name: "b", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	phone, err := domain.NewDevices(st, pool, noChanges{}, clock).Bind(ctx, u, domain.DeviceInfo{HWID: "phone-0123456789"}, false)
	if err != nil {
		t.Fatal(err)
	}
	dev, err := st.Q.GetBoundDevice(ctx, db.GetBoundDeviceParams{UserID: u.ID, Hwid: "phone-0123456789"})
	if err != nil {
		t.Fatal(err)
	}
	own, _ := st.Q.GetSlot(ctx, u.SlotID.Int64)
	stranger, _ := st.Q.GetSlot(ctx, other.SlotID.Int64)
	fi, err := st.Q.CreateNode(ctx, db.CreateNodeParams{Name: "fi", Address: "198.51.100.7:1", CreatedAt: 1, UpdatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	nl, err := st.Q.CreateNode(ctx, db.CreateNodeParams{Name: "nl", Address: "198.51.100.8:1", CreatedAt: 1, UpdatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	h := &handlers{d: Deps{Store: st, Now: clock, Users: users, NodeOnline: func() map[int64]map[string]nodeapi.Online {
		return map[int64]map[string]nodeapi.Online{
			nl.ID: {
				phone.Name:    {Conns: 1, IPs: []string{"198.51.100.2"}},
				stranger.Name: {Conns: 4, IPs: []string{"198.51.100.9"}},
			},
			fi.ID: {
				phone.Name: {Conns: 2, IPs: []string{"198.51.100.2"}},
				own.Name:   {Conns: 3, IPs: []string{"198.51.100.5", "198.51.100.1"}},
			},
			999: {own.Name: {}}, // a node that reported the slot with nothing open
		}
	}}}
	out, err := h.userOnline(ctx, &userIDInput{ID: u.ID})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(out.Body)
	want, _ := json.Marshal([]UserOnlineView{
		{NodeID: fi.ID, NodeName: "fi", IPs: []string{"198.51.100.1", "198.51.100.5"}, Conns: 3},
		{NodeID: fi.ID, NodeName: "fi", DeviceID: &dev.ID, IPs: []string{"198.51.100.2"}, Conns: 2},
		{NodeID: nl.ID, NodeName: "nl", DeviceID: &dev.ID, IPs: []string{"198.51.100.2"}, Conns: 1},
	})
	if string(got) != string(want) {
		t.Fatalf("online:\n got %s\nwant %s", got, want)
	}
	// A support operator without access to the subscribers gets the nodes and the counts,
	// not the addresses: the field is one the people mask knows.
	masked := string(maskPeople(got))
	if strings.Contains(masked, "198.51.100.") || !strings.Contains(masked, `"online_ips":[]`) || !strings.Contains(masked, `"node_name":"fi"`) {
		t.Fatalf("masked: %s", masked)
	}

	// Without nodes there is nowhere to be online: an empty list, not null.
	h.d.NodeOnline = nil
	out, err = h.userOnline(ctx, &userIDInput{ID: u.ID})
	if err != nil || out.Body == nil || len(out.Body) != 0 {
		t.Fatalf("no nodes: %v %v", out, err)
	}
}
