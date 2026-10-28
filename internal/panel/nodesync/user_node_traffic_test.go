package nodesync

import (
	"context"
	"crypto/x509"
	"testing"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/nodetls"
	"mikan/internal/panel/domain"
)

// Each user's bytes on each node are kept by the pair, from the same bytes the users'
// statistics take: a bound device's slot adds to its user, a batch delivered twice counts
// once, a slot nobody owns and a pool that is gone are left out, and the pair's last seen
// moves to the latest batch that had the user's bytes on that node only.
func TestUserNodeTrafficByPair(t *testing.T) {
	s1, node1, st, users, now := setup(t)
	ctx := context.Background()
	q := st.Q
	tariffs, _ := q.ListTariffs(ctx)
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
	a, _ := q.GetUser(ctx, ids[0])
	clock := func() time.Time { return *now }
	phone, err := domain.NewDevices(st, domain.NewPool(st, clock), s1.m, clock).Bind(ctx, a, domain.DeviceInfo{HWID: "phone-0123456789"}, false)
	if err != nil {
		t.Fatal(err)
	}
	panel, _ := nodetls.Generate("mikan-panel", x509.ExtKeyUsageClientAuth, time.Now())
	n2, _, err := domain.AddNode(ctx, st, panel, domain.NodeInput{Name: "B", Host: "198.51.100.20", APIPort: 40000}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	node2 := &fakeNode{}
	s2 := s1.m.attach(t, n2.ID, Target{Node: node2, TLS: fakeTLS})

	start := *now
	node1.batch = nodeapi.Counters{Epoch: "e1", Seq: 1,
		Slots: map[string]nodeapi.Traffic{slots[0]: {Up: 10, Down: 100}, phone.Name: {Up: 1, Down: 2}, slots[1]: {Up: 3, Down: 4}, "ghost": {Up: 7, Down: 7}},
		Pools: map[string]map[string]nodeapi.Traffic{slots[0]: {"999": {Down: 9}}}}
	s1.pullCounters(ctx)
	s1.pullCounters(ctx) // the same batch again: counted once
	*now = now.Add(time.Hour)
	node2.batch = nodeapi.Counters{Epoch: "e2", Seq: 1, Slots: map[string]nodeapi.Traffic{slots[0]: {Up: 300, Down: 3000}}}
	s2.pullCounters(ctx)
	*now = now.Add(time.Hour)
	node1.batch = nodeapi.Counters{Epoch: "e1", Seq: 2, Slots: map[string]nodeapi.Traffic{slots[1]: {Up: 20, Down: 200}}}
	s1.pullCounters(ctx)

	type pair struct{ up, down, seen int64 }
	got := map[[2]int64]pair{}
	rows, err := st.DB.QueryContext(ctx, "SELECT user_id, node_id, up, down, last_seen FROM user_node_traffic")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var u, n int64
		var p pair
		if err := rows.Scan(&u, &n, &p.up, &p.down, &p.seen); err != nil {
			t.Fatal(err)
		}
		got[[2]int64{u, n}] = p
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := map[[2]int64]pair{
		{ids[0], LocalNode}: {11, 102, start.Unix()},
		{ids[1], LocalNode}: {23, 204, start.Add(2 * time.Hour).Unix()},
		{ids[0], n2.ID}:     {300, 3000, start.Add(time.Hour).Unix()},
	}
	if len(got) != len(want) {
		t.Fatalf("pairs %v, want %v", got, want)
	}
	for k, w := range want {
		if got[k] != w {
			t.Fatalf("user %d on node %d: %+v, want %+v", k[0], k[1], got[k], w)
		}
	}
	// The pairs add up to the users' statistics.
	var pairs, stats int64
	if err := st.DB.QueryRowContext(ctx, "SELECT (SELECT SUM(up+down) FROM user_node_traffic), (SELECT SUM(up+down) FROM traffic_daily)").Scan(&pairs, &stats); err != nil || pairs != stats {
		t.Fatalf("pairs %d, statistics %d: %v", pairs, stats, err)
	}
	// A deleted node takes its pairs along.
	if _, err := st.DB.ExecContext(ctx, "DELETE FROM nodes WHERE id = $1", n2.ID); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := st.DB.QueryRowContext(ctx, "SELECT count(*) FROM user_node_traffic WHERE node_id = $1", n2.ID).Scan(&left); err != nil || left != 0 {
		t.Fatalf("%d pairs of a deleted node: %v", left, err)
	}
}
