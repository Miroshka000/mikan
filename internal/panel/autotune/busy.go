package autotune

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/audit"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

// A port another program holds on a node's server (nginx or caddy on 443, say) keeps an
// inbound from listening at all. The node names such a listener (nodeapi.ListenerStatus
// Busy), and the tuner moves the inbound to the next free port of domain.PortPool, as it
// moves a blocked one: the same rules, event, audit entry and alert. The cascade relay
// moves the same way, to the pool's next free port, with an audit entry only: events
// belong to inbounds.

// ReasonBusy is the reason of a port change made because another program held the port.
const ReasonBusy = "busy"

// Why a busy inbound stays where it is (BusyMove).
const (
	BusyStale  = "stale"   // the inbound moved since its listener failed: the node is catching up
	BusyOff    = "off"     // automatic moves are off for it, or its port is not one to move
	BusyMikan  = "mikan"   // the port is mikan's own: another inbound, the relay, the panel
	BusyNoPort = "no_port" // every port of the pool is taken or was left lately
)

// BusyMove decides where inbound x goes when its listener failed on failed, a port that
// something already holds on the node's server: the first port of domain.PortPool free
// over its network on ports, the node's port map, and not in left, the ports inbounds of
// the node left lately. It returns the port, or "" and why the inbound stays.
func BusyMove(ports domain.PortMap, x db.Inbound, failed string, portOn, anyPort bool, left map[string]bool) (port, why string) {
	switch {
	case x.Enabled == 0 || x.Port != failed:
		return "", BusyStale
	// Hopping ranges are the admin's, and so is a port a proxy in front forwards to.
	case !portOn || x.AutoPort == 0 || strings.Contains(x.Port, "-") || domain.ListenPinsPort(x.Listen):
		return "", BusyOff
	}
	network := domain.InboundNetwork(x)
	if _, mine := ports.Busy(x.Port, network, domain.InboundHolder(x)); mine {
		return "", BusyMikan
	}
	skip := map[string]bool{x.Port: true}
	for p := range left {
		skip[p] = true
	}
	free := FreePorts(ports, network, skip, anyPort)
	if len(free) == 0 {
		return "", BusyNoPort
	}
	return free[0], ""
}

// BusyRelayMove decides where node's relay, on relay, goes when its listener failed on
// failed, a port that something already holds on the node's server: the first port of
// domain.RelayPort's choice, not in left, the ports the relay left lately. It returns the
// port, or "" and why the relay stays.
func BusyRelayMove(ports domain.PortMap, relay, failed string, portOn bool, left map[string]bool) (port, why string) {
	switch {
	case relay != failed:
		return "", BusyStale
	case !portOn:
		return "", BusyOff
	}
	self := domain.PortHolder{Kind: domain.PortRelay}
	if _, mine := ports.Busy(relay, "tcp", self); mine {
		return "", BusyMikan
	}
	skip := map[string]bool{relay: true}
	for p := range left {
		skip[p] = true
	}
	p, ok := domain.RelayPort(ports, skip)
	if !ok {
		return "", BusyNoPort
	}
	return strconv.Itoa(p), ""
}

// leftPorts are the ports inbounds of node left over network within Abandon: blocked on
// the way to clients or held by another program, either way not picked again.
func (t *Tuner) leftPorts(events []db.InboundEvent, node int64, network string, now time.Time) map[string]bool {
	left := map[string]bool{}
	for _, e := range events {
		if e.NodeID == node && e.Kind == "port" && e.Network == network && now.Sub(time.Unix(e.CreatedAt, 0)) < t.o.Abandon {
			left[e.OldValue] = true
		}
	}
	return left
}

func (t *Tuner) runBusy(ctx context.Context) {
	tick := time.NewTicker(t.o.Busy)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			t.MoveBusy(ctx)
		}
	}
}

// MoveBusy moves every inbound whose listener failed because another program holds its
// port. It reads the nodes' last health, so it costs nothing while every listener is up.
// A new port that is busy too fails on the node's next apply and is moved from in turn;
// the ports left behind are not picked again, so the moves end when the pool does.
func (t *Tuner) MoveBusy(ctx context.Context) {
	t.busyMu.Lock()
	defer t.busyMu.Unlock()
	nodes, err := t.st.Q.ListNodes(ctx)
	if err != nil {
		t.log.Error("autotune: busy ports: nodes", "err", err)
		return
	}
	type failure struct{ name, port string }
	busy := map[int64][]failure{}
	for _, n := range nodes {
		hv, ok := t.nodes.Health(n.ID)
		if n.Enabled == 0 || !ok || !hv.OK {
			continue
		}
		for _, l := range hv.Listeners {
			// A listener is judged by the port the node ran it on; without one, the node runs a
			// state this panel has not applied yet.
			if p := hv.Ports[l.Name]; p != "" && l.Busy() {
				busy[n.ID] = append(busy[n.ID], failure{l.Name, p})
			}
		}
	}
	if len(busy) == 0 {
		t.forgetBusy(nil)
		return
	}
	now := t.now()
	portOn, err := t.set.On(ctx, settings.AutoPort)
	if err != nil {
		t.log.Error("autotune: busy ports: settings", "err", err)
		return
	}
	anyPort, err := t.set.On(ctx, settings.AnyPort)
	if err != nil {
		t.log.Error("autotune: busy ports: settings", "err", err)
		return
	}
	events, err := t.st.Q.ListInboundEventsSince(ctx, now.Add(-t.o.Abandon).Unix())
	if err != nil {
		t.log.Error("autotune: busy ports: events", "err", err)
		return
	}
	seen := map[int64]bool{}
	for _, n := range nodes {
		for _, f := range busy[n.ID] {
			// Read again for every listener: a move just made takes its new port.
			if f.name == nodeapi.RelayListener {
				t.moveBusyRelay(ctx, n, f.port, portOn, now)
				seen[-n.ID] = true
				continue
			}
			ports, err := t.nodePorts(ctx, n)
			if err != nil {
				t.log.Error("autotune: busy ports: node ports", "node", n.ID, "err", err)
				break
			}
			inbounds, err := t.st.Q.ListNodeInbounds(ctx, n.ID)
			if err != nil {
				t.log.Error("autotune: busy ports: inbounds", "node", n.ID, "err", err)
				break
			}
			i := slices.IndexFunc(inbounds, func(in db.Inbound) bool { return in.Name == f.name })
			if i < 0 {
				continue
			}
			x := inbounds[i]
			seen[x.ID] = true
			network := domain.InboundNetwork(x)
			port, why := BusyMove(ports, x, f.port, portOn, anyPort, t.leftPorts(events, n.ID, network, now))
			if why != "" {
				t.noteBusy(n, x.ID, x.Name, f.port, why)
				continue
			}
			// The admin or a detector round may move it meanwhile: then their port stands.
			prev, next, err := t.inbounds.Update(ctx, x.ID, domain.InboundPatch{Port: &port, FromPort: &f.port})
			if errors.Is(err, domain.ErrInboundChanged) {
				continue
			}
			if err != nil {
				t.log.Error("autotune: busy ports: move", "node", n.ID, "inbound", x.Name, "err", err)
				continue
			}
			e := t.record(ctx, now, n, next, db.AddInboundEventParams{Kind: "port", Network: network, OldValue: prev.Port, NewValue: next.Port, Reason: ReasonBusy})
			events = append(events, e)
		}
	}
	t.forgetBusy(seen)
}

// moveBusyRelay moves node's relay off port failed, which another program holds. The
// admin or an earlier move may have changed the row since: then it stays as it is.
func (t *Tuner) moveBusyRelay(ctx context.Context, n db.Node, failed string, portOn bool, now time.Time) {
	r, err := t.st.Q.GetNodeRelay(ctx, n.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return
	}
	if err != nil {
		t.log.Error("autotune: busy ports: relay", "node", n.ID, "err", err)
		return
	}
	ports, err := t.nodePorts(ctx, n)
	if err != nil {
		t.log.Error("autotune: busy ports: node ports", "node", n.ID, "err", err)
		return
	}
	port, why := BusyRelayMove(ports, r.Port, failed, portOn, t.relayLeftPorts(n.ID, now))
	if why != "" {
		t.noteBusy(n, -n.ID, nodeapi.RelayListener, failed, why)
		return
	}
	err = domain.MoveRelay(ctx, t.st, n, failed, port)
	if errors.Is(err, domain.ErrRelayChanged) {
		return
	}
	if err != nil {
		t.log.Error("autotune: busy ports: move relay", "node", n.ID, "err", err)
		return
	}
	t.mu.Lock()
	if t.relayLeft[n.ID] == nil {
		t.relayLeft[n.ID] = map[string]time.Time{}
	}
	t.relayLeft[n.ID][failed] = now
	t.mu.Unlock()
	_ = audit.Write(ctx, t.st.Q, now, audit.Entry{Action: "auto.relay_port", TargetType: "node", TargetID: strconv.FormatInt(n.ID, 10),
		Details: map[string]any{"node": n.ID, "old": failed, "new": port, "reason": ReasonBusy}})
	t.log.Warn("autotune: relay port changed", "node", n.ID, "old", failed, "new", port, "reason", ReasonBusy)
	// Every node reads the port from the row: the sources of the relay follow.
	t.changes.SlotsChanged()
}

// relayLeftPorts are the ports node's relay left within Abandon.
func (t *Tuner) relayLeftPorts(node int64, now time.Time) map[string]bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	left := map[string]bool{}
	for p, at := range t.relayLeft[node] {
		if now.Sub(at) < t.o.Abandon {
			left[p] = true
		} else {
			delete(t.relayLeft[node], p)
		}
	}
	return left
}

// noteBusy logs once why a busy listener stays: the node reports it every few seconds.
// id is the inbound's, or minus the node's for its relay.
func (t *Tuner) noteBusy(n db.Node, id int64, name, port, why string) {
	key := port + "/" + why
	t.mu.Lock()
	same := t.busy[id] == key
	t.busy[id] = key
	t.mu.Unlock()
	if same || why == BusyStale {
		return
	}
	t.log.Warn("autotune: the listener's port is held by another program; it stays", "node", n.ID, "listener", name, "port", port, "why", why)
}

// forgetBusy drops what noteBusy logged for listeners that are no longer busy.
func (t *Tuner) forgetBusy(busy map[int64]bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for id := range t.busy {
		if !busy[id] {
			delete(t.busy, id)
		}
	}
}
