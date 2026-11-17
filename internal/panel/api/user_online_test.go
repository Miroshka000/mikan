package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/nodesync"
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

// A reset asks the nodes to close the slots of the keys picked (all of the user's, its own,
// a bound device's) on every node or on one, adds up what they closed and names a node too
// old for the call without failing the rest; it goes into the audit log. Another user's
// device or an unknown node is not found.
func TestResetUserConnections(t *testing.T) {
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
	devices := domain.NewDevices(st, pool, noChanges{}, clock)
	u, err := users.Create(ctx, domain.CreateInput{Name: "a", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	other, err := users.Create(ctx, domain.CreateInput{Name: "b", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	phone, err := devices.Bind(ctx, u, domain.DeviceInfo{HWID: "phone-0123456789"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := devices.Bind(ctx, other, domain.DeviceInfo{HWID: "other-0123456789"}, false); err != nil {
		t.Fatal(err)
	}
	dev, _ := st.Q.GetBoundDevice(ctx, db.GetBoundDeviceParams{UserID: u.ID, Hwid: "phone-0123456789"})
	strange, _ := st.Q.GetBoundDevice(ctx, db.GetBoundDeviceParams{UserID: other.ID, Hwid: "other-0123456789"})
	own, _ := st.Q.GetSlot(ctx, u.SlotID.Int64)
	fi, err := st.Q.CreateNode(ctx, db.CreateNodeParams{Name: "fi", Address: "198.51.100.7:1", CreatedAt: 1, UpdatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	old, err := st.Q.CreateNode(ctx, db.CreateNodeParams{Name: "old", Address: "198.51.100.8:1", CreatedAt: 1, UpdatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	type call struct {
		node  int64
		slots []string
	}
	var calls []call
	h := &handlers{d: Deps{Store: st, Now: clock, Users: users, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		ResetSlots: func(_ context.Context, node int64, slots []string) []nodesync.ConnReset {
			calls = append(calls, call{node, slices.Sorted(slices.Values(slots))})
			out := []nodesync.ConnReset{{Node: old.ID, Err: &nodeapi.StatusError{Method: "POST", Path: "/v1/slots/reset", Status: http.StatusNotFound}},
				{Node: fi.ID, Closed: 3}, {Node: 999, Err: nodeapi.ErrUnavailable}}
			if node != 0 {
				out = []nodesync.ConnReset{{Node: node, Closed: 2}}
			}
			return out
		}}}
	reset := func(node int64, device *int64) (*resetConnectionsOutput, error) {
		in := &resetConnectionsInput{ID: u.ID}
		in.Body.NodeID, in.Body.DeviceID = node, device
		return h.resetUserConnections(ctx, in)
	}
	zero := int64(0)

	out, err := reset(0, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(out.Body)
	want := fmt.Sprintf(`{"closed":3,"nodes":[{"node_id":%d,"node_name":"fi","status":"ok","closed":3},{"node_id":%d,"node_name":"old","status":"node_too_old","closed":0},{"node_id":999,"node_name":"","status":"node_unavailable","closed":0}]}`, fi.ID, old.ID)
	if string(got) != want {
		t.Fatalf("all keys, every node:\n got %s\nwant %s", got, want)
	}
	if _, err := reset(fi.ID, &zero); err != nil {
		t.Fatal(err)
	}
	if out, err := reset(0, &dev.ID); err != nil || out.Body.Closed != 3 {
		t.Fatalf("a device: %+v %v", out, err)
	}
	wantCalls := []call{
		{0, slices.Sorted(slices.Values([]string{own.Name, phone.Name}))},
		{fi.ID, []string{own.Name}},
		{0, []string{phone.Name}},
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("asked %+v, want %+v", calls, wantCalls)
	}

	for _, c := range []struct {
		node   int64
		device *int64
	}{{0, &strange.ID}, {12345, nil}} {
		if _, err := reset(c.node, c.device); err == nil || !strings.Contains(err.Error(), "not_found") {
			t.Errorf("node %d device %v: %v", c.node, c.device, err)
		}
	}
	if len(calls) != 3 {
		t.Fatal("nothing is asked of the nodes for what is not found")
	}

	rows, err := st.Q.ListAudit(ctx, db.ListAuditParams{BeforeID: 1 << 62, Lim: 100})
	if err != nil {
		t.Fatal(err)
	}
	var details []string
	for _, r := range rows {
		if r.Action == "user.reset_connections" && r.TargetID.String == strconv.FormatInt(u.ID, 10) {
			details = append(details, r.Details.String)
		}
	}
	slices.Reverse(details) // oldest first
	wantDetails := []string{
		fmt.Sprintf(`{"closed":3,"skipped":[%d,999]}`, old.ID),
		fmt.Sprintf(`{"closed":2,"device":0,"node":%d}`, fi.ID),
		fmt.Sprintf(`{"closed":3,"device":%d,"skipped":[%d,999]}`, dev.ID, old.ID),
	}
	if !reflect.DeepEqual(details, wantDetails) {
		t.Fatalf("audit:\n got %q\nwant %q", details, wantDetails)
	}

	// Without nodes nothing is asked and nothing closed.
	h.d.ResetSlots = nil
	if out, err := reset(0, nil); err != nil || out.Body.Closed != 0 || out.Body.Nodes == nil || len(out.Body.Nodes) != 0 {
		t.Fatalf("no nodes: %+v %v", out, err)
	}
}
