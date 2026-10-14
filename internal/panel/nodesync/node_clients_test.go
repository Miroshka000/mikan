package nodesync

import (
	"context"
	"crypto/x509"
	"strconv"
	"testing"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/nodetls"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
)

// Who went through a node is counted with the node's and the users' traffic, on the same
// transaction and from the same bytes: one row per user and node, so a node's clients add
// up to what it carried and a user's rows to what the user used. A slot nobody owns is
// left out, as everywhere else.
func TestNodeClientsAddUpToTheUsers(t *testing.T) {
	s1, node1, st, users, now := setup(t)
	ctx := context.Background()
	q := st.Q
	s1.m.storeInterval = 0
	tariffs, _ := q.ListTariffs(ctx)
	pool, err := q.CreateTrafficPool(ctx, db.CreateTrafficPoolParams{Name: "WL", CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	key := strconv.FormatInt(pool.ID, 10)
	var ids []int64
	var slots []string
	for _, name := range []string{"a", "b"} {
		u, err := users.Create(ctx, domain.CreateInput{Name: name, TariffID: tariffs[1].ID})
		if err != nil {
			t.Fatal(err)
		}
		slot, _ := q.GetSlot(ctx, u.SlotID.Int64)
		ids, slots = append(ids, u.ID), append(slots, slot.Name)
	}
	panel, _ := nodetls.Generate("mikan-panel", x509.ExtKeyUsageClientAuth, time.Now())
	n2, _, err := domain.AddNode(ctx, st, panel, domain.NodeInput{Name: "B", Host: "198.51.100.20", APIPort: 40000}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	node2 := &fakeNode{}
	s2 := s1.m.attach(t, n2.ID, Target{Node: node2, TLS: fakeTLS})

	// The first node carries main and pool traffic of "a", main of "b" and a slot nobody
	// owns; the second carries "b" alone.
	node1.batch = nodeapi.Counters{Epoch: "e1", Seq: 1,
		Slots: map[string]nodeapi.Traffic{slots[0]: {Up: 10, Down: 100}, slots[1]: {Up: 1, Down: 2}, "ghost": {Up: 7, Down: 7}},
		Pools: map[string]map[string]nodeapi.Traffic{slots[0]: {key: {Up: 5, Down: 50}}},
		Online: map[string]nodeapi.Online{
			slots[0]: {IPs: []string{"203.0.113.1"}},
			slots[1]: {IPs: []string{"203.0.113.2"}},
		}}
	s1.pullCounters(ctx)
	node2.batch = nodeapi.Counters{Epoch: "e2", Seq: 1,
		Slots:  map[string]nodeapi.Traffic{slots[1]: {Up: 300, Down: 3000}},
		Online: map[string]nodeapi.Online{slots[1]: {IPs: []string{"203.0.113.3"}}}}
	s2.pullCounters(ctx)

	type row struct{ up, down int64 }
	got := map[[2]int64]row{}
	rs, err := st.DB.QueryContext(ctx, "SELECT user_id, node_id, up, down FROM node_clients")
	if err != nil {
		t.Fatal(err)
	}
	for rs.Next() {
		var k [2]int64
		var r row
		if err := rs.Scan(&k[0], &k[1], &r.up, &r.down); err != nil {
			rs.Close()
			t.Fatal(err)
		}
		got[k] = r
	}
	rs.Close()
	if err := rs.Err(); err != nil {
		t.Fatal(err)
	}
	want := map[[2]int64]row{
		{ids[0], 1}:     {15, 150}, // a on node 1: main 10/100 + pool 5/50
		{ids[1], 1}:     {1, 2},    // b on node 1
		{ids[1], n2.ID}: {300, 3000},
	}
	if len(got) != len(want) {
		t.Fatalf("node_clients has %d rows, want %d: %v", len(got), len(want), got)
	}
	for k, w := range want {
		if got[k] != w {
			t.Fatalf("user %d on node %d: %d/%d, want %d/%d", k[0], k[1], got[k].up, got[k].down, w.up, w.down)
		}
	}
	// A node's clients are what it carried; a user's rows are what the user used.
	node1Row, _ := q.GetNode(ctx, 1)
	var node1ClientsUp, node1ClientsDown int64
	if err := st.DB.QueryRowContext(ctx, "SELECT COALESCE(SUM(up),0), COALESCE(SUM(down),0) FROM node_clients WHERE node_id = 1").Scan(&node1ClientsUp, &node1ClientsDown); err != nil {
		t.Fatal(err)
	}
	if node1ClientsUp != node1Row.TotalUp || node1ClientsDown != node1Row.TotalDown {
		t.Fatalf("node 1's clients %d/%d, the node carried %d/%d", node1ClientsUp, node1ClientsDown, node1Row.TotalUp, node1Row.TotalDown)
	}
	a, _ := q.GetUser(ctx, ids[0])
	var aUp, aDown int64
	if err := st.DB.QueryRowContext(ctx, "SELECT COALESCE(SUM(up),0), COALESCE(SUM(down),0) FROM node_clients WHERE user_id = $1", ids[0]).Scan(&aUp, &aDown); err != nil {
		t.Fatal(err)
	}
	if aUp != a.TotalUp || aDown != a.TotalDown {
		t.Fatalf("user %d's rows %d/%d, the user's totals %d/%d", ids[0], aUp, aDown, a.TotalUp, a.TotalDown)
	}
	// Who a node saw online is written as "last seen" per node: the same live view, but
	// kept per node, so a user on two of them has a time for each.
	if err := s1.m.recordDevices(ctx, *now); err != nil {
		t.Fatal(err)
	}
	seen := map[[2]int64]int64{}
	rs2, err := st.DB.QueryContext(ctx, "SELECT user_id, node_id, last_seen FROM node_clients")
	if err != nil {
		t.Fatal(err)
	}
	for rs2.Next() {
		var k [2]int64
		var at int64
		if err := rs2.Scan(&k[0], &k[1], &at); err != nil {
			rs2.Close()
			t.Fatal(err)
		}
		seen[k] = at
	}
	rs2.Close()
	for _, k := range [][2]int64{{ids[0], 1}, {ids[1], 1}, {ids[1], n2.ID}} {
		if seen[k] != now.Unix() {
			t.Fatalf("user %d on node %d last seen %d, want %d", k[0], k[1], seen[k], now.Unix())
		}
	}
	// A user who was never online on a node keeps no row for it: "a" is on node 1 only.
	if _, ok := seen[[2]int64{ids[0], n2.ID}]; ok {
		t.Fatalf("a row for a user that was never on node %d", n2.ID)
	}
}
