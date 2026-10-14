package infraalerts

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/autotune"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/hysteresis"
	"mikan/internal/panel/nodesync"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

type busyNodes struct{ health nodesync.HealthView }

func (busyNodes) Activity(context.Context, int64) (nodeapi.Activity, error) {
	return nodeapi.Activity{}, nodeapi.ErrUnavailable
}
func (busyNodes) CheckTarget(context.Context, int64, nodeapi.TargetCheckRequest) (nodeapi.TargetResult, error) {
	return nodeapi.TargetResult{}, nodeapi.ErrUnavailable
}
func (busyNodes) ScanTargets(context.Context, int64, nodeapi.TargetScanRequest) (nodeapi.TargetScan, error) {
	return nodeapi.TargetScan{}, nodeapi.ErrUnavailable
}
func (b busyNodes) Health(id int64) (nodesync.HealthView, bool) { return b.health, id == 1 }

type noChanges struct{}

func (noChanges) PoliciesChanged() {}
func (noChanges) SlotsChanged()    {}

// From the node's health to the admin's chat: nginx holds 443, the tuner moves XHTTP, and
// the alert names the inbound and both ports. Off for inbound events, no alert.
func TestBusyPortMoveAlertsTheAdmin(t *testing.T) {
	st, ctx := testMonitorStore(t)
	now := time.Unix(1_800_000_000, 0)
	clock := func() time.Time { return now }
	if err := domain.Seed(ctx, st, now); err != nil {
		t.Fatal(err)
	}
	set := settings.New(st.Q)
	ins, err := st.Q.ListNodeInbounds(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	hv := nodesync.HealthView{OK: true, Ports: map[string]string{}, CheckedAt: now}
	for _, in := range ins {
		hv.Ports[in.Name] = in.Port
		ls := nodeapi.ListenerStatus{Name: in.Name, OK: true}
		if in.Name == "vless-xhttp" {
			ls = nodeapi.ListenerStatus{Name: in.Name, Error: "listen tcp :443: bind: address already in use", Code: nodeapi.ListenerAddrInUse}
		}
		hv.Listeners = append(hv.Listeners, ls)
	}
	m := New(st, set, nil, nil, nil, nil, nil, slog.Default(), clock)
	state, err := m.load(ctx) // the cursor starts after what happened before
	if err != nil {
		t.Fatal(err)
	}
	tn := autotune.New(st, set, busyNodes{hv}, noChanges{}, slog.New(slog.NewTextHandler(io.Discard, nil)), clock, autotune.DefaultOptions())
	tn.MoveBusy(ctx)

	nodes, err := st.Q.ListNodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	nodeByID := map[int64]db.Node{}
	for _, n := range nodes {
		nodeByID[n.ID] = n
	}
	byID := map[int64]db.Inbound{}
	for _, in := range ins {
		byID[in.ID] = in
	}
	off := state
	cfg := Default()
	m.autotuneEvents(ctx, &state, cfg, nodeByID, byID, "en")
	if len(state.Pending) != 1 {
		t.Fatalf("pending: %+v", state.Pending)
	}
	text := state.Pending[0].Text
	for _, want := range []string{"vless-xhttp", "from port 443 to 2053", "held by another program", nodeByID[1].Name} {
		if !strings.Contains(text, want) {
			t.Fatalf("alert %q lacks %q", text, want)
		}
	}
	if strings.Contains(text, "—") {
		t.Fatalf("alert %q has an em dash", text)
	}
	cfg.Events.Inbound = false
	m.autotuneEvents(ctx, &off, cfg, nodeByID, byID, "en")
	if len(off.Pending) != 0 || off.AutoCursor != state.AutoCursor {
		t.Fatalf("inbound events off: pending %+v, cursor %d of %d", off.Pending, off.AutoCursor, state.AutoCursor)
	}
}

// A busy port the tuner may move is given time to move before the admin hears of it; a
// port it may not move is reported as quickly as any other failure.
func TestBusyInboundWaitsForTheMove(t *testing.T) {
	if inboundFailAfter(true, true) <= inboundFailAfter(false, true) {
		t.Fatal("a movable busy port is reported as fast as any failure")
	}
	if inboundFailAfter(true, false) != inboundFailAfter(false, false) {
		t.Fatal("a busy port with automatic moves off waits for a move that never comes")
	}
}

// A relay that cannot listen is reported like an inbound that cannot: a plain failure
// after a few samples, one whose port another program holds only when the tuner's move
// did not help (and at once when it does not move it), and its recovery follows.
func TestRelayListenerAlerts(t *testing.T) {
	st, _ := testMonitorStore(t)
	now := time.Unix(1_800_000_000, 0)
	m := New(st, nil, nil, nil, nil, nil, nil, slog.Default(), func() time.Time { return now })
	node := db.Node{ID: 7, Name: "US"}
	state := persistentState{Samples: map[string]sampleState{}}
	round := func(l nodeapi.ListenerStatus, moves bool, lang string) hysteresis.Level {
		now = now.Add(5 * time.Second)
		l.Name = nodeapi.RelayListener
		hv := nodesync.HealthView{OK: true, CheckedAt: now, Listeners: []nodeapi.ListenerStatus{{Name: "vless", OK: true}, l}}
		return m.checkRelay(&state, Default(), node, hv, moves, lang)
	}
	failed := nodeapi.ListenerStatus{Error: "listen tcp 0.0.0.0:2053: bind: permission denied"}
	busy := nodeapi.ListenerStatus{Error: "listen tcp 0.0.0.0:2053: bind: address already in use", Code: nodeapi.ListenerAddrInUse}

	// A node without a relay says nothing.
	hv := nodesync.HealthView{OK: true, CheckedAt: now, Listeners: []nodeapi.ListenerStatus{{Name: "vless", OK: true}}}
	if lv := m.checkRelay(&state, Default(), node, hv, true, "en"); lv != hysteresis.Healthy || len(state.Pending) != 0 {
		t.Fatalf("no relay: %v %+v", lv, state.Pending)
	}
	for range 2 {
		if lv := round(failed, true, "en"); lv != hysteresis.Unavailable {
			t.Fatalf("level %v", lv)
		}
	}
	if len(state.Pending) != 0 {
		t.Fatalf("reported too soon: %+v", state.Pending)
	}
	round(failed, true, "en")
	if len(state.Pending) != 1 || !strings.Contains(state.Pending[0].Text, "US / cascade relay") || !strings.Contains(state.Pending[0].Text, "Unavailable") {
		t.Fatalf("pending: %+v", state.Pending)
	}
	for range 2 {
		round(nodeapi.ListenerStatus{OK: true}, true, "ru")
	}
	if len(state.Pending) != 2 || !strings.Contains(state.Pending[1].Text, "US / служебный вход каскада") || !strings.Contains(state.Pending[1].Text, "Восстановлен") {
		t.Fatalf("recovery: %+v", state.Pending)
	}

	// A busy port the tuner moves is given time; with moves off it is reported at once.
	state = persistentState{Samples: map[string]sampleState{}}
	for range 23 {
		round(busy, true, "en")
	}
	if len(state.Pending) != 0 {
		t.Fatalf("a busy relay reported before the move had its time: %+v", state.Pending)
	}
	round(busy, true, "en")
	if len(state.Pending) != 1 {
		t.Fatalf("a busy relay the move did not help: %+v", state.Pending)
	}
	state = persistentState{Samples: map[string]sampleState{}}
	for range 3 {
		round(busy, false, "en")
	}
	if len(state.Pending) != 1 {
		t.Fatalf("a busy relay with moves off: %+v", state.Pending)
	}

	// The relay's alerts are the exit events': off, nothing is queued.
	state = persistentState{Samples: map[string]sampleState{}}
	cfg := Default()
	cfg.Events.Exit = false
	for range 3 {
		now = now.Add(5 * time.Second)
		hv := nodesync.HealthView{OK: true, CheckedAt: now, Listeners: []nodeapi.ListenerStatus{{Name: nodeapi.RelayListener, Error: "x"}}}
		m.checkRelay(&state, cfg, node, hv, false, "en")
	}
	if len(state.Pending) != 0 {
		t.Fatalf("exit events off: %+v", state.Pending)
	}
}
