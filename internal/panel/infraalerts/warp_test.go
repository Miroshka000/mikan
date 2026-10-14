package infraalerts

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/hysteresis"
	"mikan/internal/panel/nodesync"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

type warpRuntime struct {
	status nodeapi.WarpStatus
	force  []bool
}

func (*warpRuntime) Health(int64) (nodesync.HealthView, bool) { return nodesync.HealthView{}, false }
func (r *warpRuntime) Warp(_ context.Context, _ int64, force bool) (nodeapi.WarpStatus, error) {
	r.force = append(r.force, force)
	return r.status, nil
}
func (*warpRuntime) Probe(context.Context, int64, string) (nodeapi.ProbeResult, error) {
	return nodeapi.ProbeResult{}, nodeapi.ErrUnavailable
}

// The WARP alert says why it failed, in the admin's language, and never forces a check:
// the monitor polls every cycle.
func TestWarpAlertNamesTheReason(t *testing.T) {
	st, ctx := testMonitorStore(t)
	now := time.Unix(1_800_000_000, 0)
	if err := domain.Seed(ctx, st, now); err != nil {
		t.Fatal(err)
	}
	if err := st.Q.SaveNodeWarp(ctx, db.SaveNodeWarpParams{NodeID: 1, Source: "import", PrivateKey: "priv", PeerPublicKey: "pub",
		Endpoint: "162.159.192.1:2408", Ipv4: "172.16.0.2", Mtu: 1280, Routes: "[]", CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	nodes, err := st.Q.ListNodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for lang, want := range map[string][]string{
		"en": {"Reason: no answer from the WARP endpoint 162.159.192.1:2408 over UDP", "WARP"},
		"ru": {"Причина: нет ответа от endpoint 162.159.192.1:2408 по UDP", "WARP"},
	} {
		t.Run(lang, func(t *testing.T) {
			rt := &warpRuntime{}
			m := New(st, settings.New(st.Q), rt, nil, nil, nil, nil, slog.Default(), func() time.Time { return now })
			cfg := Default()
			state := persistentState{Samples: map[string]sampleState{}}
			// Two failed rounds make the alert.
			for i := 0; i < 2; i++ {
				rt.status = nodeapi.WarpStatus{Configured: true, Error: "timeout", Detail: "ignored for a known code", CheckedAt: now.Add(time.Duration(i+1) * time.Minute)}
				m.probeWarp(ctx, &state, cfg, nodes, nil, map[int64]hysteresis.Level{}, lang)
			}
			if len(state.Pending) != 1 {
				t.Fatalf("pending: %+v", state.Pending)
			}
			for _, w := range want {
				if !strings.Contains(state.Pending[0].Text, w) {
					t.Fatalf("alert %q lacks %q", state.Pending[0].Text, w)
				}
			}
			for _, f := range rt.force {
				if f {
					t.Fatal("the monitor forced a check")
				}
			}
		})
	}
}

// A code the panel does not know falls back to the node's own words, and an old node's
// bare "unreachable" still reads as a reason.
func TestWarpReasonFallbacks(t *testing.T) {
	for name, tc := range map[string]struct {
		s    nodeapi.WarpStatus
		lang string
		want string
	}{
		"detail":      {nodeapi.WarpStatus{Error: "failed", Detail: "boom"}, "en", "Reason: boom"},
		"old node":    {nodeapi.WarpStatus{Error: "unreachable"}, "en", "Reason: unreachable"},
		"nothing":     {nodeapi.WarpStatus{}, "ru", "Причина: проверка не прошла"},
		"https":       {nodeapi.WarpStatus{Error: "https_timeout"}, "en", "Reason: the tunnel is up"},
		"html in the": {nodeapi.WarpStatus{Error: "failed", Detail: "<b>"}, "en", "Reason: <b>"}, // escaped by the caller
	} {
		t.Run(name, func(t *testing.T) {
			if got := warpReason(tc.s, "1.2.3.4:2408", tc.lang); !strings.HasPrefix(got, tc.want) {
				t.Fatalf("%q, want prefix %q", got, tc.want)
			}
		})
	}
}
