package infraalerts

import (
	"context"
	"database/sql"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/hysteresis"
	"mikan/internal/panel/nodesync"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

type probeRuntime struct {
	mu    sync.Mutex
	asked []string // "node/proxy"
	ok    bool
	at    time.Time
}

func (*probeRuntime) Health(int64) (nodesync.HealthView, bool) { return nodesync.HealthView{}, false }
func (*probeRuntime) Warp(context.Context, int64, bool) (nodeapi.WarpStatus, error) {
	return nodeapi.WarpStatus{}, nodeapi.ErrUnavailable
}
func (r *probeRuntime) Probe(_ context.Context, id int64, proxy string) (nodeapi.ProbeResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asked = append(r.asked, strconv.FormatInt(id, 10)+"/"+proxy)
	return nodeapi.ProbeResult{OK: r.ok, CheckedAt: r.at}, nil
}

// An inbound through another node is stored as outbound "direct" with its exit node: the
// monitor probes that exit. A relay set to go on to a node while nobody leaves through it
// does not run, and is not probed.
func TestProbeExitsAsksTheExitsInUse(t *testing.T) {
	st, ctx := testMonitorStore(t)
	now := time.Unix(1_800_000_000, 0)
	if err := domain.Seed(ctx, st, now); err != nil {
		t.Fatal(err)
	}
	q := st.Q
	mk := func(name, host string) db.Node {
		n, err := q.CreateNode(ctx, db.CreateNodeParams{Name: name, Address: host + ":40000", PublicHost: host, CreatedAt: now.Unix(), UpdatedAt: now.Unix()})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	b, c := mk("B", "198.51.100.20"), mk("C", "198.51.100.30")
	ins, err := q.ListNodeInbounds(ctx, 1)
	if err != nil || len(ins) == 0 || ins[0].Enabled == 0 {
		t.Fatalf("the seeded inbound: %+v %v", ins, err)
	}
	if err := domain.UseExit(ctx, q, 1, b.ID, now, nil); err != nil {
		t.Fatal(err)
	}
	if err := q.SetInboundExit(ctx, db.SetInboundExitParams{ExitNodeID: sql.NullInt64{Int64: b.ID, Valid: true}, Outbound: "direct", ID: ins[0].ID}); err != nil {
		t.Fatal(err)
	}
	// C's relay goes on to B, and nobody leaves through C.
	if _, err := domain.EnsureRelay(ctx, q, c, now, nil); err != nil {
		t.Fatal(err)
	}
	if err := domain.UseExit(ctx, q, c.ID, b.ID, now, nil); err != nil {
		t.Fatal(err)
	}
	if err := q.SetNodeRelayRoute(ctx, db.SetNodeRelayRouteParams{Outbound: "direct", ExitNodeID: sql.NullInt64{Int64: b.ID, Valid: true}, NodeID: c.ID}); err != nil {
		t.Fatal(err)
	}

	rt := &probeRuntime{at: now}
	m := New(st, settings.New(q), rt, nil, nil, nil, nil, slog.Default(), func() time.Time { return now })
	nodes, err := q.ListNodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	all, err := q.ListInbounds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byNode := map[int64][]db.Inbound{}
	for _, in := range all {
		byNode[in.NodeID] = append(byNode[in.NodeID], in)
	}
	state := persistentState{Samples: map[string]sampleState{}}
	cfg := Default()
	round := func(ok bool, at time.Time) {
		rt.mu.Lock()
		rt.asked, rt.ok, rt.at = nil, ok, at
		rt.mu.Unlock()
		m.probeExits(ctx, &state, cfg, nodes, byNode, map[int64]hysteresis.Level{}, "en")
	}
	round(true, now)
	want := []string{"1/" + nodeapi.ExitName(b.ID)}
	if !slices.Equal(rt.asked, want) {
		t.Fatalf("probed %v, want %v", rt.asked, want)
	}
	// The exit fails: after probeFailAfter rounds the alert is there.
	for i := range probeFailAfter {
		round(false, now.Add(time.Duration(i+1)*time.Minute))
	}
	if len(state.Pending) == 0 {
		t.Fatalf("no alert for a failing exit: %+v", state.Samples)
	}
}
