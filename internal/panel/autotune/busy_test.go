package autotune

import (
	"bytes"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/nodesync"
	"mikan/internal/panel/presets"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/subs"
)

const inUse = "listen tcp :443: bind: address already in use"

// runs: the node runs the inbounds of node at their ports now, and these listeners failed.
func (e *env) runs(t *testing.T, node int64, failed ...nodeapi.ListenerStatus) {
	t.Helper()
	ins, err := e.st.Q.ListNodeInbounds(e.ctx, node)
	if err != nil {
		t.Fatal(err)
	}
	hv := nodesync.HealthView{OK: true, Ports: map[string]string{}, CheckedAt: e.now}
	for _, in := range ins {
		if in.Enabled == 0 {
			continue
		}
		hv.Ports[in.Name] = in.Port
		ls := nodeapi.ListenerStatus{Name: in.Name, OK: true}
		for _, f := range failed {
			if f.Name == in.Name {
				ls = f
			}
		}
		hv.Listeners = append(hv.Listeners, ls)
	}
	if e.nodes.health == nil {
		e.nodes.health = map[int64]nodesync.HealthView{}
	}
	e.nodes.health[node] = hv
}

func busyOn(name string) nodeapi.ListenerStatus {
	return nodeapi.ListenerStatus{Name: name, Error: inUse, Code: nodeapi.ListenerAddrInUse}
}

func (e *env) ports(t *testing.T, node int64) domain.PortMap {
	t.Helper()
	n, err := e.st.Q.GetNode(e.ctx, node)
	if err != nil {
		t.Fatal(err)
	}
	m, err := domain.NodePorts(e.ctx, e.st.Q, n)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// The decision: a port something outside mikan holds moves to the first free port of the
// pool over the same network; anything else stays.
func TestBusyMove(t *testing.T) {
	e := setup(t)
	xhttp, hy2 := e.inbound(t, "vless-xhttp"), e.inbound(t, "hysteria2")
	// A hopping range over UDP takes 2053..2100 of the pool there, not over TCP.
	config, err := presets.NewConfig("hysteria2", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Q.CreateInbound(e.ctx, db.CreateInboundParams{NodeID: 1, Name: "hopping", Preset: "hysteria2", Port: "2000-2100", Config: config}); err != nil {
		t.Fatal(err)
	}
	ports := e.ports(t, 1)
	with := func(in db.Inbound, f func(*db.Inbound)) db.Inbound { f(&in); return in }
	allPool := map[string]bool{}
	for _, p := range domain.PortPool {
		allPool[strconv.Itoa(p)] = true
	}
	for _, c := range []struct {
		name   string
		in     db.Inbound
		failed string
		portOn bool
		left   map[string]bool
		port   string
		why    string
	}{
		{name: "tcp", in: xhttp, failed: "443", portOn: true, port: "2053"},
		{name: "udp past the hopping range", in: hy2, failed: "443", portOn: true, port: "2443"},
		{name: "ports left lately", in: xhttp, failed: "443", portOn: true, left: map[string]bool{"2053": true, "2083": true}, port: "2087"},
		{name: "pool exhausted", in: xhttp, failed: "443", portOn: true, left: allPool, why: BusyNoPort},
		{name: "the row moved since", in: xhttp, failed: "8443", portOn: true, why: BusyStale},
		{name: "disabled", in: with(xhttp, func(i *db.Inbound) { i.Enabled = 0 }), failed: "443", portOn: true, why: BusyStale},
		{name: "moves off in settings", in: xhttp, failed: "443", why: BusyOff},
		{name: "moves off for the inbound", in: with(xhttp, func(i *db.Inbound) { i.AutoPort = 0 }), failed: "443", portOn: true, why: BusyOff},
		{name: "behind a proxy", in: with(xhttp, func(i *db.Inbound) { i.Listen = "127.0.0.1" }), failed: "443", portOn: true, why: BusyOff},
		{name: "a hopping range", in: with(hy2, func(i *db.Inbound) { i.Port = "20000-20100" }), failed: "20000-20100", portOn: true, why: BusyOff},
		// The panel's own HTTPS is mikan's, not another program: the admin's conflict to fix.
		{name: "the panel's port", in: with(xhttp, func(i *db.Inbound) { i.Port = "21355" }), failed: "21355", portOn: true, why: BusyMikan},
		{name: "another inbound's port", in: with(xhttp, func(i *db.Inbound) { i.Port = "8443" }), failed: "8443", portOn: true, why: BusyMikan},
	} {
		port, why := BusyMove(ports, c.in, c.failed, c.portOn, false, c.left)
		if port != c.port || why != c.why {
			t.Errorf("%s: got %q %q, want %q %q", c.name, port, why, c.port, c.why)
		}
	}
}

// nginx holds 443/tcp and caddy 443/udp: XHTTP and Hysteria2 move to the pool by
// themselves, with an event, an audit entry and a push to the node; other failures stay.
func TestBusyInboundMovesByItself(t *testing.T) {
	e := setup(t)
	var logs bytes.Buffer
	e.tn.log = slog.New(slog.NewTextHandler(&logs, nil))
	// Hysteria2 comes from a node before 0.5: no code, only the OS's words.
	e.runs(t, 1, busyOn("vless-xhttp"), nodeapi.ListenerStatus{Name: "hysteria2", Error: "listen udp :443: bind: address already in use"},
		nodeapi.ListenerStatus{Name: "tuic", Error: "listen udp :8443: bind: permission denied"})
	e.tn.MoveBusy(e.ctx)

	if got := e.inbound(t, "vless-xhttp").Port; got != "2053" {
		t.Fatalf("XHTTP on %s, want 2053", got)
	}
	if got := e.inbound(t, "hysteria2").Port; got != "2053" {
		t.Fatalf("Hysteria2 on %s, want 2053 over UDP", got)
	}
	if got := e.inbound(t, "tuic").Port; got != "8443" {
		t.Fatalf("a failure other than a busy port moved TUIC to %s", got)
	}
	ev := e.events(t)
	if len(ev) != 2 {
		t.Fatalf("events: %+v", ev)
	}
	for _, x := range ev {
		if x.Kind != "port" || x.Reason != ReasonBusy || x.OldValue != "443" || x.NewValue != "2053" || x.NodeID != 1 {
			t.Fatalf("event: %+v", x)
		}
	}
	var audits int
	if err := e.st.DB.QueryRowContext(e.ctx, `SELECT count(*) FROM audit_log WHERE action = 'auto.inbound_port' AND details LIKE '%"reason":"busy"%'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 2 {
		t.Fatalf("audit entries: %d", audits)
	}
	if e.ch.slots == 0 {
		t.Fatal("the node was not told")
	}

	// The node has not applied the move yet: its health still names 443, which is no longer
	// the row's port. Nothing moves twice.
	e.tn.MoveBusy(e.ctx)
	if got := e.inbound(t, "vless-xhttp").Port; got != "2053" || len(e.events(t)) != 2 {
		t.Fatalf("moved again on a stale health: %s, %d events", got, len(e.events(t)))
	}

	// Subscriptions are built from the rows: clients get the new port.
	slot, err := e.st.Q.GetSlot(e.ctx, mustUser(t, e).SlotID.Int64)
	if err != nil {
		t.Fatal(err)
	}
	links, err := subs.URIs(subs.Profile{Slot: slot, Inbounds: []db.Inbound{e.inbound(t, "vless-xhttp")},
		Nodes: []subs.Node{{ID: 1, Endpoint: subs.Endpoint{Host: "203.0.113.10"}}}})
	if err != nil || !strings.Contains(links, "@203.0.113.10:2053?") {
		t.Fatalf("subscription: %s %v", links, err)
	}

	// 2053 is held too: the next apply fails there and the inbound moves on, never back to
	// a port it left. Once the pool runs out it stays with the error showing.
	tcpFree := len(FreePorts(e.ports(t, 1), "tcp", nil, false)) + 1 // and the 2053 it is on
	for range 2 * len(domain.PortPool) {
		e.runs(t, 1, busyOn("vless-xhttp"))
		e.tn.MoveBusy(e.ctx)
	}
	seen := map[string]bool{"443": true}
	moves := 0
	for _, x := range e.events(t) {
		if x.InboundID != e.inbound(t, "vless-xhttp").ID {
			continue
		}
		if seen[x.NewValue] {
			t.Fatalf("moved back to %s: %+v", x.NewValue, e.events(t))
		}
		seen[x.NewValue] = true
		moves++
	}
	if moves != tcpFree {
		t.Fatalf("%d moves, want one per free TCP port of the pool: %d", moves, tcpFree)
	}
	if n := strings.Count(logs.String(), "it stays"); n != 1 {
		t.Fatalf("an exhausted pool is logged %d times, want once:\n%s", n, logs.String())
	}
	if !strings.Contains(logs.String(), "why="+BusyNoPort) {
		t.Fatalf("log: %s", logs.String())
	}
}

func mustUser(t *testing.T, e *env) db.User {
	t.Helper()
	u, err := e.st.Q.GetUser(e.ctx, e.user)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// A remote node's inbounds move the same way; its node API port is never picked.
func TestBusyInboundMovesOnARemoteNode(t *testing.T) {
	e := setup(t)
	n, err := e.st.Q.CreateNode(e.ctx, db.CreateNodeParams{Name: "US", Address: "203.0.113.7:2053", PublicHost: "203.0.113.7", CreatedAt: e.now.Unix(), UpdatedAt: e.now.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	in, err := domain.NewInbounds(e.st, nil, func() time.Time { return e.now }).Create(e.ctx, domain.NewInbound{NodeID: n.ID, Preset: "vless_reality_xhttp", Port: "443"})
	if err != nil {
		t.Fatal(err)
	}
	e.runs(t, n.ID, busyOn(in.Name))
	e.tn.MoveBusy(e.ctx)
	got, err := e.st.Q.GetInbound(e.ctx, in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Port != "2083" {
		t.Fatalf("remote inbound on %s, want 2083 (2053 is the node API's)", got.Port)
	}
	if local := e.inbound(t, "vless-xhttp").Port; local != "443" {
		t.Fatalf("the panel's node moved too: %s", local)
	}
}

// Off in settings: the inbound stays, the error stays visible, nothing is recorded.
func TestBusyInboundStaysWithMovesOff(t *testing.T) {
	e := setup(t)
	if err := settings.Set(e.ctx, e.set, settings.KeyAutoPort, false); err != nil {
		t.Fatal(err)
	}
	e.runs(t, 1, busyOn("vless-xhttp"))
	e.tn.MoveBusy(e.ctx)
	if got := e.inbound(t, "vless-xhttp").Port; got != "443" || len(e.events(t)) != 0 {
		t.Fatalf("moved with moves off: %s", got)
	}
}

// A move off a busy port gives clients time to catch up, like any port change, but it is
// no sign of DPI: it leaves the budget of automatic changes alone.
func TestBusyMoveIsNotAChangeAgainstTheBudget(t *testing.T) {
	e := setup(t)
	at := e.now.Add(-time.Hour).Unix()
	w := &world{now: e.now, events: []db.InboundEvent{
		{InboundID: 7, Kind: "port", Reason: ReasonBusy, CreatedAt: at},
		{InboundID: 7, Kind: "port", Reason: ReasonBusy, CreatedAt: at},
		{InboundID: 7, Kind: "sni", Reason: "target_down", CreatedAt: at},
	}}
	h := e.tn.history(w, 7)
	if h.recent != 1 || h.lastPort.Unix() != at {
		t.Fatalf("history: %+v", h)
	}
}

// runsRelay: node 1 runs its relay at the row's port now, with this status, and its
// server holds the ports of host (nil: the node does not say).
func (e *env) runsRelay(t *testing.T, status nodeapi.ListenerStatus, host *nodeapi.HostPorts) {
	t.Helper()
	r, err := e.st.Q.GetNodeRelay(e.ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	e.runs(t, 1)
	hv := e.nodes.health[1]
	status.Name = nodeapi.RelayListener
	hv.Ports[nodeapi.RelayListener] = r.Port
	hv.Listeners = append(hv.Listeners, status)
	hv.Health.Host = host
	e.nodes.health[1] = hv
}

func (e *env) relay(t *testing.T) string {
	t.Helper()
	r, err := e.st.Q.GetNodeRelay(e.ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	return r.Port
}

func (e *env) relayAudits(t *testing.T) int {
	t.Helper()
	var n int
	if err := e.st.DB.QueryRowContext(e.ctx, `SELECT count(*) FROM audit_log WHERE action = 'auto.relay_port' AND details LIKE '%"reason":"busy"%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The relay's decision, like an inbound's: the first free port of the pool for TCP that
// the relay did not leave lately, and one the server's own programs do not hold.
func TestBusyRelayMove(t *testing.T) {
	e := setup(t)
	ports := e.ports(t, 1)
	allPool := map[string]bool{}
	for _, p := range domain.PortPool {
		allPool[strconv.Itoa(p)] = true
	}
	// Every high port held too: nothing is left to pick.
	var high []int
	for p := 30000; p < 60000; p++ {
		high = append(high, p)
	}
	for _, c := range []struct {
		name          string
		host          *nodeapi.HostPorts
		relay, failed string
		portOn        bool
		left          map[string]bool
		port, why     string
	}{
		{name: "the pool's first", relay: "2053", failed: "2053", portOn: true, port: "2083"},
		{name: "ports left lately", relay: "2053", failed: "2053", portOn: true, left: map[string]bool{"2083": true, "2087": true}, port: "2096"},
		{name: "held by programs of the server", host: &nodeapi.HostPorts{TCP: []int{2083}, UDP: []int{2087}}, relay: "2053", failed: "2053", portOn: true, port: "2087"},
		{name: "the whole pool left", relay: "2053", failed: "2053", portOn: true, left: allPool, port: "high"},
		{name: "nothing left to pick", host: &nodeapi.HostPorts{TCP: high}, relay: "2053", failed: "2053", portOn: true, left: allPool, why: BusyNoPort},
		{name: "the row moved since", relay: "2083", failed: "2053", portOn: true, why: BusyStale},
		{name: "moves off in settings", relay: "2053", failed: "2053", why: BusyOff},
		// The panel's own HTTPS is mikan's, not another program: the admin's conflict to fix.
		{name: "the panel's port", relay: "21355", failed: "21355", portOn: true, why: BusyMikan},
		{name: "an inbound's port", relay: "443", failed: "443", portOn: true, why: BusyMikan},
	} {
		port, why := BusyRelayMove(ports.WithHost(c.host), c.relay, c.failed, c.portOn, c.left)
		if c.port == "high" {
			if p, err := strconv.Atoi(port); err != nil || p < 30000 || why != "" {
				t.Errorf("%s: got %q %q, want a high port", c.name, port, why)
			}
			continue
		}
		if port != c.port || why != c.why {
			t.Errorf("%s: got %q %q, want %q %q", c.name, port, why, c.port, c.why)
		}
	}
}

// A third-party Xray holds the relay's 2053/tcp: the relay moves on by itself, with an
// audit entry and a push to the nodes, so its sources follow; a failure of any other kind
// stays; and the relay never goes back to a port it left.
func TestBusyRelayMovesByItself(t *testing.T) {
	e := setup(t)
	var logs bytes.Buffer
	e.tn.log = slog.New(slog.NewTextHandler(&logs, nil))
	local, err := e.st.Q.GetNode(e.ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if r, err := domain.EnsureRelay(e.ctx, e.st.Q, local, e.now, nil); err != nil || r.Port != "2053" {
		t.Fatalf("relay %+v, %v", r, err)
	}
	// Something else than a busy port: nothing moves.
	e.runsRelay(t, nodeapi.ListenerStatus{Error: "listen tcp :2053: bind: permission denied"}, nil)
	e.tn.MoveBusy(e.ctx)
	if got := e.relay(t); got != "2053" || e.relayAudits(t) != 0 {
		t.Fatalf("a failure that is not a busy port moved the relay to %s", got)
	}

	// The node tells what its server holds: 2083 is not an option either. A node before 0.5
	// sends no code, only the OS's words.
	e.runsRelay(t, nodeapi.ListenerStatus{Error: inUse}, &nodeapi.HostPorts{TCP: []int{2053, 2083}})
	e.tn.MoveBusy(e.ctx)
	if got := e.relay(t); got != "2087" {
		t.Fatalf("relay on %s, want 2087", got)
	}
	if e.relayAudits(t) != 1 || e.ch.slots == 0 {
		t.Fatalf("audit entries %d, the nodes told %d times", e.relayAudits(t), e.ch.slots)
	}
	if len(e.events(t)) != 0 {
		t.Fatalf("the relay is no inbound's event: %+v", e.events(t))
	}
	e.tn.MoveBusy(e.ctx) // the health still names 2053: the node has not caught up
	if got := e.relay(t); got != "2087" || e.relayAudits(t) != 1 {
		t.Fatalf("moved again on a stale health: %s, %d entries", got, e.relayAudits(t))
	}

	// 2087 is held as well, and the node does not say: on to the first one not left.
	e.runsRelay(t, busyOn(nodeapi.RelayListener), nil)
	e.tn.MoveBusy(e.ctx)
	if got := e.relay(t); got != "2083" {
		t.Fatalf("relay on %s, want 2083", got)
	}
	seen := map[string]bool{"2053": true, "2087": true, "2083": true}
	for range 2 * len(domain.PortPool) {
		cur := e.relay(t)
		e.runsRelay(t, busyOn(nodeapi.RelayListener), nil)
		e.tn.MoveBusy(e.ctx)
		if next := e.relay(t); next != cur {
			if seen[next] {
				t.Fatalf("the relay went back to %s", next)
			}
			seen[next] = true
		}
	}

	// Moves off in settings: the relay stays and the error stays visible.
	if err := settings.Set(e.ctx, e.set, settings.KeyAutoPort, false); err != nil {
		t.Fatal(err)
	}
	before, audits := e.relay(t), e.relayAudits(t)
	e.runsRelay(t, busyOn(nodeapi.RelayListener), nil)
	e.tn.MoveBusy(e.ctx)
	if e.relay(t) != before || e.relayAudits(t) != audits {
		t.Fatalf("moved with moves off: %s", e.relay(t))
	}
}
