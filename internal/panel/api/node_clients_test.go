package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/store/storetest"
	"mikan/internal/panel/support"
)

// A node's clients are the users with bytes on it, the latest seen first, a page at a time
// and by a part of the name; each says what of its keys is open on the node now. A user's
// nodes are the other way round. The names and addresses are the people mask's, and the
// search is closed to an operator who may not see the subscribers.
func TestNodeClientsAndUserNodes(t *testing.T) {
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
	var us []db.User
	for _, name := range []string{"Alice", "Bob", "Joanna", "Stranger"} {
		u, err := users.Create(ctx, domain.CreateInput{Name: name, TariffID: tariffs[1].ID})
		if err != nil {
			t.Fatal(err)
		}
		us = append(us, u)
	}
	a, b, c, stranger := us[0], us[1], us[2], us[3]
	phone, err := domain.NewDevices(st, pool, noChanges{}, clock).Bind(ctx, a, domain.DeviceInfo{HWID: "phone-0123456789"}, false)
	if err != nil {
		t.Fatal(err)
	}
	dev, _ := st.Q.GetBoundDevice(ctx, db.GetBoundDeviceParams{UserID: a.ID, Hwid: "phone-0123456789"})
	slot := func(u db.User) string { s, _ := st.Q.GetSlot(ctx, u.SlotID.Int64); return s.Name }
	fi, _ := st.Q.CreateNode(ctx, db.CreateNodeParams{Name: "fi", Address: "198.51.100.7:1", CreatedAt: 1, UpdatedAt: 1})
	nl, _ := st.Q.CreateNode(ctx, db.CreateNodeParams{Name: "nl", Address: "198.51.100.8:1", CreatedAt: 1, UpdatedAt: 1})
	for _, r := range []struct{ user, node, up, down, seen int64 }{
		{a.ID, fi.ID, 10, 100, 300}, {b.ID, fi.ID, 20, 200, 200}, {c.ID, fi.ID, 30, 300, 100},
		{a.ID, nl.ID, 1, 2, 400},
	} {
		if _, err := st.DB.ExecContext(ctx, "INSERT INTO user_node_traffic(user_id,node_id,up,down,last_seen) VALUES($1,$2,$3,$4,$5)", r.user, r.node, r.up, r.down, r.seen); err != nil {
			t.Fatal(err)
		}
	}
	h := &handlers{d: Deps{Store: st, Now: clock, Users: users, NodeOnline: func() map[int64]map[string]nodeapi.Online {
		return map[int64]map[string]nodeapi.Online{
			fi.ID: {
				phone.Name:     {Conns: 2, IPs: []string{"198.51.100.2"}},
				slot(a):        {Conns: 3, IPs: []string{"198.51.100.5", "198.51.100.1"}},
				slot(stranger): {Conns: 1, IPs: []string{"198.51.100.9"}}, // online, no bytes stored yet
				slot(c):        {},                                        // reported with nothing open
			},
			nl.ID: {slot(b): {Conns: 1, IPs: []string{"198.51.100.3"}}},
		}
	}}}

	list := func(ctx context.Context, q string, limit, offset int32) *nodeClientsOutput {
		t.Helper()
		out, err := h.nodeClients(ctx, &nodeClientsInput{ID: fi.ID, Query: q, Limit: limit, Offset: offset})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	out := list(ctx, "", 50, 0)
	got, _ := json.Marshal(out.Body)
	devID := dev.ID
	want, _ := json.Marshal(struct {
		Items  []NodeClient `json:"items"`
		Total  int64        `json:"total"`
		Online int          `json:"online"`
	}{Items: []NodeClient{
		{UserID: a.ID, UserName: "Alice", Up: 10, Down: 100, LastSeen: time.Unix(300, 0).UTC(), Online: []NodeClientOnline{
			{IPs: []string{"198.51.100.1", "198.51.100.5"}, Conns: 3},
			{DeviceID: &devID, IPs: []string{"198.51.100.2"}, Conns: 2},
		}},
		{UserID: b.ID, UserName: "Bob", Up: 20, Down: 200, LastSeen: time.Unix(200, 0).UTC(), Online: []NodeClientOnline{}},
		{UserID: c.ID, UserName: "Joanna", Up: 30, Down: 300, LastSeen: time.Unix(100, 0).UTC(), Online: []NodeClientOnline{}},
	}, Total: 3, Online: 2})
	if string(got) != string(want) {
		t.Fatalf("clients:\n got %s\nwant %s", got, want)
	}
	if page := list(ctx, "", 1, 1); page.Body.Total != 3 || len(page.Body.Items) != 1 || page.Body.Items[0].UserID != b.ID {
		t.Fatalf("the second page of one: %+v", page.Body)
	}
	if found := list(ctx, " ANN ", 50, 0); found.Body.Total != 1 || len(found.Body.Items) != 1 || found.Body.Items[0].UserID != c.ID {
		t.Fatalf("by a part of the name: %+v", found.Body)
	}
	if _, err := h.nodeClients(ctx, &nodeClientsInput{ID: 999, Limit: 50}); err == nil {
		t.Fatal("clients of a node that does not exist")
	} else if status, _, _ := refusalOf(err); status != http.StatusNotFound {
		t.Fatalf("a node that does not exist: %d", status)
	}

	// An operator without the subscribers: the list, masked, but no search by name.
	blind := context.WithValue(ctx, keySupport, supportCtx{grant: db.SupportGrant{Level: support.LevelView}})
	if _, err := h.nodeClients(blind, &nodeClientsInput{ID: fi.ID, Query: "ann", Limit: 50}); err == nil {
		t.Fatal("an operator without the subscribers searched a node's clients by name")
	} else if status, _, code := refusalOf(err); status != http.StatusForbidden || code != "support_denied" {
		t.Fatalf("the search: %d %s", status, code)
	}
	plain, _ := json.Marshal(list(blind, "", 50, 0).Body)
	masked := string(maskPeople(plain))
	for _, leak := range []string{"Alice", "Bob", "Joanna", "198.51.100."} {
		if strings.Contains(masked, leak) {
			t.Fatalf("masked clients name %q: %s", leak, masked)
		}
	}
	if !strings.Contains(masked, `"user_name":"#`) || !strings.Contains(masked, `"online_ips":[]`) || !strings.Contains(masked, `"conns":3`) {
		t.Fatalf("masked clients: %s", masked)
	}
	seeing := context.WithValue(ctx, keySupport, supportCtx{grant: db.SupportGrant{Level: support.LevelView, ShowPeople: true}})
	if found := list(seeing, "ann", 50, 0); found.Body.Total != 1 {
		t.Fatalf("an operator who sees the subscribers searched: %+v", found.Body)
	}

	// The user's nodes, the latest seen first, with where the user is online now.
	nodes, err := h.userNodes(ctx, &userIDInput{ID: a.ID})
	if err != nil {
		t.Fatal(err)
	}
	got, _ = json.Marshal(nodes.Body)
	want, _ = json.Marshal([]UserNode{
		{NodeID: nl.ID, NodeName: "nl", Up: 1, Down: 2, LastSeen: time.Unix(400, 0).UTC()},
		{NodeID: fi.ID, NodeName: "fi", Up: 10, Down: 100, LastSeen: time.Unix(300, 0).UTC(), Online: true},
	})
	if string(got) != string(want) {
		t.Fatalf("user's nodes:\n got %s\nwant %s", got, want)
	}
	if masked := string(maskPeople(got)); !strings.Contains(masked, `"node_name":"fi"`) {
		t.Fatalf("the mask took the node's name: %s", masked)
	}
	if none, err := h.userNodes(ctx, &userIDInput{ID: stranger.ID}); err != nil || none.Body == nil || len(none.Body) != 0 {
		t.Fatalf("a user with no bytes anywhere: %v %v", none, err)
	}
	if _, err := h.userNodes(ctx, &userIDInput{ID: 999_999}); err == nil {
		t.Fatal("the nodes of a user that does not exist")
	}

	// Both only read: a view operator and a read key reach them, under the nodes' and the
	// users' tags.
	ops := registeredOps(t)
	for id, tag := range map[string]string{"node-clients": "node", "user-nodes": "users"} {
		op := ops[id]
		if op == nil || op.Method != http.MethodGet || len(op.Tags) != 1 || op.Tags[0] != tag {
			t.Fatalf("%s: %+v", id, op)
		}
		if status, code := supportGate(op, false, db.SupportGrant{Level: support.LevelView}); status != 0 {
			t.Fatalf("%s at view: %d %s", id, status, code)
		}
	}
}
