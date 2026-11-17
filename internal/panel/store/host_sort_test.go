package store

import (
	"context"
	"slices"
	"testing"

	"mikan/internal/panel/store/db"
)

// A panel that updates to the places of its servers (migration 76) keeps the order its
// subscriptions had: the nodes in their order, a node's inbounds by id, an inbound's hosts
// by position. Every host gets a place of its own.
func TestHostSortMigrationKeepsTheOrder(t *testing.T) {
	ctx := context.Background()
	s, err := OpenTest(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p, err := postgresProvider(ctx, s.DB, postgresFS)
	if err != nil {
		t.Fatal(err)
	}
	const migration = 76
	before := int64(0)
	for _, src := range p.ListSources() {
		if src.Version < migration {
			before = src.Version
		}
	}
	if _, err := p.DownTo(ctx, before); err != nil {
		t.Fatal(err)
	}
	var has bool
	if err := s.DB.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='inbound_hosts' AND column_name='sort')").Scan(&has); err != nil || has {
		t.Fatalf("inbound_hosts.sort after the rollback: %v %v", has, err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.DB.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	// Node 30 goes first by its order, node 20 second; node 20's inbounds come by id.
	exec("INSERT INTO nodes(id,name,created_at,updated_at,sort) VALUES (20,'a',1,1,2), (30,'b',1,1,1)")
	exec("INSERT INTO inbounds(id,node_id,name,preset,port,created_at,updated_at) VALUES (110,20,'x','custom','1',1,1), (105,20,'y','custom','2',1,1), (108,30,'z','custom','3',1,1)")
	host := func(inbound, position int64) int64 {
		t.Helper()
		var id int64
		if err := s.DB.QueryRowContext(ctx, "INSERT INTO inbound_hosts(inbound_id,position) VALUES ($1,$2) RETURNING id", inbound, position).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	// Written out of their order on purpose.
	h105b, h108a, h110a, h108c, h105a, h108b := host(105, 1), host(108, 0), host(110, 0), host(108, 2), host(105, 0), host(108, 1)
	if _, err := p.Up(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Q.ListHostsInOrder(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ids, sorts []int64
	for _, r := range rows {
		ids, sorts = append(ids, r.InboundHost.ID), append(sorts, r.InboundHost.Sort)
	}
	if want := []int64{h108a, h108b, h108c, h105a, h105b, h110a}; !slices.Equal(ids, want) {
		t.Fatalf("order after the migration: %v, want %v", ids, want)
	}
	if !slices.Equal(sorts, []int64{1, 2, 3, 4, 5, 6}) {
		t.Fatalf("places after the migration: %v", sorts)
	}
}

// A new host takes the place of its inbound's last one, right after it; a new inbound that
// of its node's last server; the first one of a node without servers the place after all; a
// copy's hosts the place of its default one. Places the admin set stay as they are.
func TestNewHostsTakeTheirPlaces(t *testing.T) {
	ctx := context.Background()
	s, err := OpenTest(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	q := s.Q
	node := func(name string) db.Node {
		t.Helper()
		n, err := q.CreateNode(ctx, db.CreateNodeParams{Name: name, CreatedAt: 1, UpdatedAt: 1})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	// inbound adds an inbound of n with its default host, as domain does.
	inbound := func(n db.Node, name string) (db.Inbound, int64) {
		t.Helper()
		in, err := q.CreateInbound(ctx, db.CreateInboundParams{NodeID: n.ID, Name: name, Preset: "custom", Port: "443", Config: "{}", CreatedAt: 1, UpdatedAt: 1})
		if err != nil {
			t.Fatal(err)
		}
		if err := q.SetDefaultInboundHost(ctx, DefaultHost(in.ID, in.Config)); err != nil {
			t.Fatal(err)
		}
		hosts, err := q.ListInboundHosts(ctx, in.ID)
		if err != nil || len(hosts) != 1 {
			t.Fatalf("default host: %v %v", hosts, err)
		}
		return in, hosts[0].ID
	}
	add := func(in db.Inbound, address string) int64 {
		t.Helper()
		h, err := q.AddInboundHost(ctx, db.AddInboundHostParams{InboundID: in.ID, Address: address})
		if err != nil {
			t.Fatal(err)
		}
		return h.ID
	}
	order := func() []int64 {
		t.Helper()
		rows, err := q.ListHostsInOrder(ctx)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]int64, len(rows))
		for i, r := range rows {
			out[i] = r.InboundHost.ID
		}
		return out
	}
	check := func(what string, want ...int64) {
		t.Helper()
		if got := order(); !slices.Equal(got, want) {
			t.Fatalf("%s: %v, want %v", what, got, want)
		}
	}

	nl, de := node("nl"), node("de")
	nlA, a0 := inbound(nl, "a")
	nlB, b0 := inbound(nl, "b")
	deC, c0 := inbound(de, "c")
	check("nodes in their order", a0, b0, c0)

	// The admin puts de's server first.
	for i, id := range []int64{c0, a0, b0} {
		if err := q.SetHostSort(ctx, db.SetHostSortParams{Sort: int64(i + 1), ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	a1 := add(nlA, "a1.example.com")
	check("a host after its inbound's last", c0, a0, a1, b0)
	c1 := add(deC, "c1.example.com")
	check("de's own after de's", c0, c1, a0, a1, b0)
	a2 := add(nlA, "a2.example.com")
	check("again after the inbound's last", c0, c1, a0, a1, a2, b0)
	nlD, d0 := inbound(nl, "d")
	check("a new inbound after its node's last server", c0, c1, a0, a1, a2, b0, d0)
	_, e0 := inbound(de, "e")
	check("de's new inbound after de's last", c0, c1, e0, a0, a1, a2, b0, d0)
	us := node("us")
	_, f0 := inbound(us, "f")
	check("a node without servers: after all", c0, c1, e0, a0, a1, a2, b0, d0, f0)

	// A copy of nlA on de: its default host after de's last server, its other hosts after it.
	cp, g0 := inbound(de, "a-copy")
	if err := q.CopyInboundHosts(ctx, db.CopyInboundHostsParams{ToInbound: cp.ID, FromInbound: nlA.ID}); err != nil {
		t.Fatal(err)
	}
	hosts, err := q.ListInboundHosts(ctx, cp.ID)
	if err != nil || len(hosts) != 3 {
		t.Fatalf("copy: %v %v", hosts, err)
	}
	check("a copy after its node's last, its hosts after its default", c0, c1, e0, g0, hosts[1].ID, hosts[2].ID, a0, a1, a2, b0, d0, f0)
	_ = nlB
	_ = nlD
}
