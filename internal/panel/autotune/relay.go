package autotune

import (
	"context"
	"strconv"
	"strings"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/audit"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/presets"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/scan"
)

// The cascade relays' camouflage. A relay the panel keeps by itself (auto_sni) shows the
// site its node's protocols show (domain.RelayTarget) and follows them every round, whoever
// changed theirs. On a node without such a site, a relay some node leaves through gets a
// site next to the server from a scan: at once while it still shows the default
// www.microsoft.com, and again when its own site fails TargetFails checks in a row. A
// replacement waits Cooldown after the last one. A relay the admin set stays as it is.

// Why a relay's site changed (the audit entry's reason).
const (
	relayReasonProtocol = "protocol" // the node's protocols show another site
	relayReasonDefault  = "default"  // it showed the default site
	relayReasonDown     = "target_down"
)

func (t *Tuner) relayTargets(ctx context.Context, w *world, checks bool) {
	relays, err := t.st.Q.ListNodeRelays(ctx)
	if err != nil {
		t.log.Error("autotune: relays", "err", err)
		return
	}
	nodes := map[int64]db.Node{}
	for _, n := range w.nodes {
		nodes[n.ID] = n
	}
	var cas *domain.Cascade
	live := func(id int64) bool {
		if cas == nil {
			var all []db.Inbound
			for _, ins := range w.inbounds {
				all = append(all, ins...)
			}
			cas = domain.NewCascade(w.nodes, all, relays)
		}
		return cas.RelayLive(id)
	}
	for _, r := range relays {
		n, ok := nodes[r.NodeID]
		if !ok || r.AutoSni == 0 || ctx.Err() != nil {
			continue
		}
		dest, sni := domain.RelayTargetOf(r)
		if want, wantSNI, ok := domain.RelayTarget(w.inbounds[n.ID]); ok {
			if !strings.EqualFold(dest, want) || !strings.EqualFold(sni, wantSNI) {
				t.setRelayTarget(ctx, w, n, sni, want, wantSNI, relayReasonProtocol)
			}
			continue
		}
		// Nothing of the node's to follow: the relay's own site, for a relay in use. Unused, it
		// does not run, and a scan would be for nothing.
		if !live(n.ID) {
			continue
		}
		if strings.EqualFold(dest, presets.DefaultDest) {
			if t.relayPickDue(n.ID, w.now) {
				t.pickRelayTarget(ctx, w, n, dest, sni, relayReasonDefault)
			}
			continue
		}
		if !checks {
			continue
		}
		res, err := t.nodes.CheckTarget(ctx, n.ID, nodeapi.TargetCheckRequest{Dest: dest, SNI: sni})
		if err != nil {
			t.log.Debug("autotune: check relay target", "node", n.ID, "err", err)
			continue
		}
		if t.noteRelayTarget(n.ID, dest, res, w.now) >= t.o.TargetFails && t.relayPickDue(n.ID, w.now) {
			t.pickRelayTarget(ctx, w, n, dest, sni, relayReasonDown)
		}
	}
}

// relayPickDue: no scan for node's relay within Cooldown, so a server with no site around
// is not scanned every round.
func (t *Tuner) relayPickDue(node int64, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	at, ok := t.relayPicked[node]
	return !ok || now.Sub(at) >= t.o.Cooldown
}

// noteRelayTarget records a check of a relay's site and returns its failures in a row.
func (t *Tuner) noteRelayTarget(node int64, dest string, r nodeapi.TargetResult, now time.Time) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if r.OK {
		t.relayFails[node] = 0
		return 0
	}
	t.relayFails[node]++
	t.failed[strings.ToLower(dest)] = now
	return t.relayFails[node]
}

// pickRelayTarget points node's relay at the best working site next to the server.
func (t *Tuner) pickRelayTarget(ctx context.Context, w *world, n db.Node, oldDest, oldSNI, reason string) {
	t.mu.Lock()
	t.relayPicked[n.ID] = w.now
	t.mu.Unlock()
	ip, err := t.nodeIP(ctx, w, n)
	if err != nil {
		t.log.Warn("autotune: relay target: node address", "node", n.ID, "err", err)
		return
	}
	wide, err := t.set.On(ctx, settings.WideScan)
	if err != nil {
		t.log.Warn("autotune: relay target: settings", "node", n.ID, "err", err)
		return
	}
	limit := 16
	if wide {
		limit = scan.WideEnough
	}
	found, err := t.nodes.ScanTargets(ctx, n.ID, nodeapi.TargetScanRequest{IP: ip, Limit: limit, Wide: wide})
	if err != nil {
		t.log.Warn("autotune: relay target: scan", "node", n.ID, "err", err)
		return
	}
	pick := t.chooseTarget(w, found.Results, oldDest, []string{oldSNI}, nil)
	if pick == nil {
		t.log.Info("autotune: relay target: no site next to the server", "node", n.ID, "scanned", found.Scanned)
		return
	}
	t.setRelayTarget(ctx, w, n, oldSNI, pick.Dest, pick.SNI, reason)
}

// setRelayTarget moves node's relay to a site, unless the admin took the relay over since
// the round began, and pushes it to the nodes: the relay and its sources read it from the row.
func (t *Tuner) setRelayTarget(ctx context.Context, w *world, n db.Node, oldSNI, dest, sni, reason string) {
	r, err := t.st.Q.GetNodeRelay(ctx, n.ID)
	if err != nil || r.AutoSni == 0 {
		return
	}
	changed, err := domain.SetRelayTarget(ctx, t.st.Q, n.ID, dest, sni, true)
	if err != nil {
		t.log.Error("autotune: relay target", "node", n.ID, "err", err)
		return
	}
	if !changed {
		return
	}
	t.mu.Lock()
	t.relayFails[n.ID] = 0
	t.mu.Unlock()
	_ = audit.Write(ctx, t.st.Q, w.now, audit.Entry{Action: "auto.relay_sni", TargetType: "node", TargetID: strconv.FormatInt(n.ID, 10),
		Details: map[string]any{"node": n.ID, "old": oldSNI, "new": sni, "reason": reason}})
	t.log.Warn("autotune: relay site changed", "node", n.ID, "old", oldSNI, "new", sni, "reason", reason)
	t.changes.SlotsChanged()
}
