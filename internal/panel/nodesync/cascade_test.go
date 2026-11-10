package nodesync

import (
	"context"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/nodetls"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

// A chain client → A (local) → B → C → internet: A sends one inbound to B, B's relay
// goes on to C, C's relay leaves directly. Each hop gets only its own part.
func TestCascadeChain(t *testing.T) {
	a, _, st, _, _ := setup(t)
	ctx := context.Background()
	q := st.Q
	if err := settings.Set(ctx, settings.New(q), settings.KeyPublicHost, "203.0.113.10"); err != nil {
		t.Fatal(err)
	}
	panel, _ := nodetls.Generate("mikan-panel", x509.ExtKeyUsageClientAuth, time.Now())
	nb, _, err := domain.AddNode(ctx, st, panel, domain.NodeInput{Name: "B", Host: "198.51.100.20", APIPort: 40000}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	nc, _, err := domain.AddNode(ctx, st, panel, domain.NodeInput{Name: "C", Host: "198.51.100.30", APIPort: 40000}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	b := a.m.attach(t, nb.ID, Target{Node: &fakeNode{}, TLS: fakeTLS})
	c := a.m.attach(t, nc.ID, Target{Node: &fakeNode{}, TLS: fakeTLS})
	use := func(src, exit int64) {
		t.Helper()
		if err := domain.CheckExit(ctx, q, src, exit); err != nil {
			t.Fatal(err)
		}
		x, err := q.GetNode(ctx, exit)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := domain.EnsureRelay(ctx, q, x, time.Now(), nil); err != nil {
			t.Fatal(err)
		}
		if _, err := domain.RelayUser(ctx, q, exit, src); err != nil {
			t.Fatal(err)
		}
	}
	ins, _ := q.ListInbounds(ctx)
	var mine db.Inbound
	for _, in := range ins {
		if in.NodeID == a.id {
			mine = in
			break
		}
	}
	use(a.id, nb.ID)
	if err := q.SetInboundExit(ctx, db.SetInboundExitParams{ExitNodeID: nullID(nb.ID), Outbound: "direct", ID: mine.ID}); err != nil {
		t.Fatal(err)
	}
	use(nb.ID, nc.ID)
	if err := q.SetNodeRelayRoute(ctx, db.SetNodeRelayRouteParams{Outbound: "direct", ExitNodeID: nullID(nc.ID), NodeID: nb.ID}); err != nil {
		t.Fatal(err)
	}
	// C sending its relay to B would loop B → C → B.
	if err := domain.CheckExit(ctx, q, nc.ID, nb.ID); !errors.Is(err, domain.ErrExitCycle) {
		t.Fatalf("cycle: %v", err)
	}
	if err := domain.CheckExit(ctx, q, a.id, a.id); !errors.Is(err, domain.ErrExitSelf) {
		t.Fatalf("self: %v", err)
	}

	da, _ := a.desired(ctx)
	if da.Relay != nil || len(da.Exits) != 1 || da.Exits[0].Name != nodeapi.ExitName(nb.ID) || len(da.Exits[0].Inbounds) != 1 || da.Exits[0].Inbounds[0] != mine.Name {
		t.Fatalf("A: relay %v exits %+v", da.Relay, da.Exits)
	}
	var proxy map[string]any
	_ = json.Unmarshal(da.Exits[0].Proxy, &proxy)
	if proxy["type"] != "vless" || proxy["server"] != "198.51.100.20" || proxy["reality-opts"] == nil {
		t.Fatalf("A's way to B: %v", proxy)
	}
	if ro, _ := proxy["reality-opts"].(map[string]any); ro["support-x25519mlkem768"] != true {
		t.Fatalf("the relay's key exchange must be post-quantum: %v", ro)
	}
	db_, _ := b.desired(ctx)
	if db_.Relay == nil || len(db_.Relay.Users) != 1 || db_.Relay.Users[0].Name != domain.RelayUserName(a.id) ||
		len(db_.Exits) != 1 || db_.Exits[0].Inbounds[0] != nodeapi.RelayListener {
		t.Fatalf("B: relay %+v exits %+v", db_.Relay, db_.Exits)
	}
	dc, _ := c.desired(ctx)
	if dc.Relay == nil || len(dc.Relay.Users) != 1 || dc.Relay.Users[0].Name != domain.RelayUserName(nb.ID) || len(dc.Exits) != 0 {
		t.Fatalf("C: relay %+v exits %+v", dc.Relay, dc.Exits)
	}
	// The keys differ per hop.
	if db_.Relay.Users[0].UUID == dc.Relay.Users[0].UUID {
		t.Fatal("one key for two hops")
	}

	// C switched off: B's relay still goes to "C", which cannot be reached — no leak.
	n, _ := q.GetNode(ctx, nc.ID)
	if _, err := q.UpdateNode(ctx, db.UpdateNodeParams{Name: n.Name, Address: n.Address, PublicHost: n.PublicHost, Domain: n.Domain, PublicName: n.PublicName, Enabled: 0, ID: n.ID}); err != nil {
		t.Fatal(err)
	}
	db_, _ = b.desired(ctx)
	_ = json.Unmarshal(db_.Exits[0].Proxy, &proxy)
	if len(db_.Exits) != 1 || proxy["server"] != "192.0.2.1" {
		t.Fatalf("an exit that is off must fail closed: %v", proxy)
	}
}

func nullID(id int64) sql.NullInt64 { return sql.NullInt64{Int64: id, Valid: true} }

// A key at a relay stays when its node stops leaving through it, so that it is the same
// when the node comes back. Until then the relay does not run, and its way on to the next
// node, set in the Cascade window, is not built either: the node check would knock on a
// port nobody listens on and ask about an exit the node does not have.
func TestRelayWithoutSourcesDoesNotRun(t *testing.T) {
	a, _, st, _, _ := setup(t)
	ctx := context.Background()
	q := st.Q
	panel, _ := nodetls.Generate("mikan-panel", x509.ExtKeyUsageClientAuth, time.Now())
	nb, _, err := domain.AddNode(ctx, st, panel, domain.NodeInput{Name: "B", Host: "198.51.100.20", APIPort: 40000}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	nc, _, err := domain.AddNode(ctx, st, panel, domain.NodeInput{Name: "C", Host: "198.51.100.30", APIPort: 40000}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	b := a.m.attach(t, nb.ID, Target{Node: &fakeNode{}, TLS: fakeTLS})
	// The Cascade window of B: its relay goes on to C.
	if err := domain.UseExit(ctx, q, nb.ID, nc.ID, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	x, _ := q.GetNode(ctx, nb.ID)
	if _, err := domain.EnsureRelay(ctx, q, x, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	if err := q.SetNodeRelayRoute(ctx, db.SetNodeRelayRouteParams{Outbound: "direct", ExitNodeID: nullID(nc.ID), NodeID: nb.ID}); err != nil {
		t.Fatal(err)
	}
	// A once left through B: its key is there, its inbound leaves directly now.
	if err := domain.UseExit(ctx, q, a.id, nb.ID, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	ins, _ := q.ListNodeInbounds(ctx, a.id)
	mine := ins[0]
	if mine.Enabled == 0 || mine.ExitNodeID.Valid {
		t.Fatalf("the local inbound: %+v", mine)
	}
	if users, _ := q.ListRelayUsers(ctx, nb.ID); len(users) != 1 {
		t.Fatalf("B's keys: %+v", users)
	}
	a.m.changes.Add(1) // the inbounds changed behind the snapshot's back
	d, err := b.desired(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if d.Relay != nil || len(d.Exits) != 0 {
		t.Fatalf("B without sources: relay %+v exits %+v", d.Relay, d.Exits)
	}
	// A comes back with the same key: the relay and its way on to C are built again.
	if err := q.SetInboundExit(ctx, db.SetInboundExitParams{ExitNodeID: nullID(nb.ID), Outbound: "direct", ID: mine.ID}); err != nil {
		t.Fatal(err)
	}
	key, _ := q.GetRelayUser(ctx, db.GetRelayUserParams{ExitNodeID: nb.ID, SrcNodeID: a.id})
	a.m.changes.Add(1)
	d, err = b.desired(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if d.Relay == nil || len(d.Relay.Users) != 1 || d.Relay.Users[0].UUID != key || len(d.Exits) != 1 || d.Exits[0].Name != nodeapi.ExitName(nc.ID) {
		t.Fatalf("B with A back: relay %+v exits %+v", d.Relay, d.Exits)
	}
	// A's inbound switched off: no source again.
	if _, err := st.DB.ExecContext(ctx, "UPDATE inbounds SET enabled = 0 WHERE id = $1", mine.ID); err != nil {
		t.Fatal(err)
	}
	a.m.changes.Add(1)
	if d, _ = b.desired(ctx); d.Relay != nil || len(d.Exits) != 0 {
		t.Fatalf("B with A's inbound off: relay %+v exits %+v", d.Relay, d.Exits)
	}
}

// The health view names the relay's port like an inbound's, so a failed relay listener is
// read against the port it failed on, and carries what the node said listens on its
// server: the panel keeps the relay's port and the moved inbounds' off those.
func TestHealthCarriesTheRelayPortAndHostPorts(t *testing.T) {
	s, node, st, _, _ := setup(t)
	ctx := context.Background()
	q := st.Q
	panel, _ := nodetls.Generate("mikan-panel", x509.ExtKeyUsageClientAuth, time.Now())
	nb, _, err := domain.AddNode(ctx, st, panel, domain.NodeInput{Name: "B", Host: "198.51.100.20", APIPort: 40000}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	local, err := q.GetNode(ctx, s.id)
	if err != nil {
		t.Fatal(err)
	}
	relay, err := domain.EnsureRelay(ctx, q, local, time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := domain.RelayUser(ctx, q, s.id, nb.ID); err != nil {
		t.Fatal(err)
	}
	// B leaves through the local node: the relay runs.
	bIn, err := q.CreateInbound(ctx, db.CreateInboundParams{NodeID: nb.ID, Name: "b-vless", Preset: "vless_reality_xhttp", Port: "8443", Config: "{}", CreatedAt: 1, UpdatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.SetInboundExit(ctx, db.SetInboundExitParams{ExitNodeID: nullID(s.id), Outbound: "direct", ID: bIn.ID}); err != nil {
		t.Fatal(err)
	}
	s.applyState(ctx)
	rev := node.applied[len(node.applied)-1].Revision
	host := &nodeapi.HostPorts{TCP: []int{22, 2083}, UDP: []int{443}}
	node.health = nodeapi.Health{Revision: rev, Host: host}
	s.refreshHealth(ctx)
	hv := s.Health()
	if hv.Ports[nodeapi.RelayListener] != relay.Port || hv.Ports["vless-xhttp"] != "443" {
		t.Fatalf("ports of the applied state: %v, the relay is on %s", hv.Ports, relay.Port)
	}
	if got := hv.HostPorts(); got == nil || !got.Listens("tcp", 2083) || !got.Listens("udp", 443) || got.Listens("tcp", 443) {
		t.Fatalf("host ports: %+v", got)
	}
	// A node before 0.5.0.2 says nothing; one that does not answer is no source of ports.
	node.health = nodeapi.Health{Revision: rev}
	s.refreshHealth(ctx)
	if got := s.Health().HostPorts(); got != nil {
		t.Fatalf("host ports of a node that says none: %+v", got)
	}
	if got := (HealthView{Health: nodeapi.Health{Host: host}}).HostPorts(); got != nil {
		t.Fatalf("host ports of a node that did not answer: %+v", got)
	}
}
