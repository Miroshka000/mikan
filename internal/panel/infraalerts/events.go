package infraalerts

import (
	"context"
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"

	"mikan/internal/panel/autotune"
	"mikan/internal/panel/store/db"
)

// certExpiryWarning is how long before the TLS certificate expires it is called a problem.
const certExpiryWarning = 7 * 24 * time.Hour

// torrentFetch is how many of the blocker's new catches a round reads; torrentShown is how
// many catches one message lists, the rest are counted.
const (
	torrentFetch = 200
	torrentShown = 10
)

// autotuneEvents names the nodes and inbounds from the round's lists: an event of a node
// removed since is passed over, as before. A move off a port another program held is an
// inbound's event: the inbound did not listen at all until then.
func (m *Monitor) autotuneEvents(ctx context.Context, st *persistentState, cfg AlertsConfig, nodes map[int64]db.Node, inbounds map[int64]db.Inbound, lang string) {
	events, err := m.store.Q.InboundEventsAfter(ctx, st.AutoCursor)
	if err != nil {
		m.logError("infrastructure alerts: autotune events", err)
		return
	}
	t := textsFor(lang)
	for _, e := range events {
		st.AutoCursor = e.ID
		if e.Reason == autotune.ReasonBusy {
			n, ok := nodes[e.NodeID]
			in, known := inbounds[e.InboundID]
			if !cfg.Events.Inbound || !ok || !known {
				continue
			}
			text := fmt.Sprintf(t.inboundMoved,
				html.EscapeString(n.Name), html.EscapeString(in.Name), html.EscapeString(e.OldValue), html.EscapeString(e.NewValue))
			st.Pending = appendPending(st.Pending, delivery{Key: fmt.Sprintf("autotune/%d", e.ID), Target: targetAdmin, Text: text})
			continue
		}
		if !cfg.Events.Autotune {
			continue
		}
		n, ok := nodes[e.NodeID]
		if !ok {
			continue
		}
		st.Pending = appendPending(st.Pending, delivery{Key: fmt.Sprintf("autotune/%d", e.ID), Target: targetAdmin,
			Text: "🛠 <b>" + t.autotuneChanged + "</b>: " + html.EscapeString(n.Name) + " / " + html.EscapeString(e.Kind) + " — " + html.EscapeString(e.OldValue) + " → " + html.EscapeString(e.NewValue)})
	}
}

func (m *Monitor) checkTuner(st *persistentState, cfg AlertsConfig, inbounds map[int64][]db.Inbound, nodes map[int64]db.Node, lang string) {
	if m.tuner == nil {
		return
	}
	t := textsFor(lang)
	for nodeID, list := range inbounds {
		for _, in := range list {
			status, ok := m.tuner.Status(in.ID)
			if !ok {
				continue
			}
			key := fmt.Sprintf("%d/%d", nodeID, in.ID)
			prev := st.Stuck[key]
			if status.Stuck == prev {
				continue
			}
			st.Stuck[key] = status.Stuck
			if autotuneFailure(status.Stuck) && cfg.Events.AutotuneRecovery {
				node, ok := nodes[nodeID]
				if !ok {
					continue
				}
				text := fmt.Sprintf(t.autotuneStuck, html.EscapeString(node.Name), html.EscapeString(in.Name), html.EscapeString(status.Stuck))
				st.Pending = appendPending(st.Pending, delivery{Key: "autotune-stuck/" + key + "/" + status.Stuck, Target: targetAdmin, Text: text})
			}
		}
	}
}

func autotuneFailure(s string) bool { return s == "no_port" || s == "no_target" || s == "exhausted" }

func (m *Monitor) checkCertificate(st *persistentState, cfg AlertsConfig, lang string) {
	if m.cert == nil || !cfg.Events.TLS {
		return
	}
	s := m.cert()
	bad := s.Error != "" || (!s.NotAfter.IsZero() && s.NotAfter.Sub(m.now()) < certExpiryWarning)
	if bad == st.TLSBad {
		return
	}
	st.TLSBad = bad
	t := textsFor(lang)
	text := t.tlsBad
	if !bad {
		text = t.tlsRecovered
	}
	if s.Error != "" {
		text += ": " + html.EscapeString(s.Error)
	}
	st.Pending = appendPending(st.Pending, delivery{Key: fmt.Sprintf("tls/%t/%d", bad, s.CheckedAt.Unix()), Target: targetAdmin, Text: text})
}

func (m *Monitor) checkUpdate(st *persistentState, cfg AlertsConfig, lang string) {
	if m.updates == nil || !cfg.Events.Update {
		return
	}
	s, ok := m.updates.Host()
	if !ok || s.At == "" || s.State == "running" || s.At == st.UpdateAt {
		return
	}
	st.UpdateAt = s.At
	if s.State != "failed" {
		return
	}
	text := textsFor(lang).updateFailed
	if s.Error != "" {
		text += ": " + html.EscapeString(s.Error)
	}
	if s.From != "" {
		text += " (" + html.EscapeString(s.From) + " → " + html.EscapeString(s.Version) + ")"
	}
	st.Pending = appendPending(st.Pending, delivery{Key: "update/" + s.At, Target: targetAdmin, Text: text})
}

// checkNodeUpdates tells about each failed update of a node once, by the time it was asked
// for. The panel stops its rollout at the failure; the admin decides what to do next.
func (m *Monitor) checkNodeUpdates(ctx context.Context, st *persistentState, cfg AlertsConfig, nodes map[int64]db.Node, lang string) {
	if m.nodeUpdates == nil || !cfg.Events.Update {
		return
	}
	t := textsFor(lang)
	failures := m.nodeUpdates.Failures(ctx)
	for _, f := range failures {
		n, ok := nodes[f.NodeID]
		id := strconv.FormatInt(f.NodeID, 10)
		if !ok || st.NodeUpdateAt[id] == f.At {
			continue
		}
		if st.NodeUpdateAt == nil {
			st.NodeUpdateAt = map[string]int64{}
		}
		st.NodeUpdateAt[id] = f.At
		text := t.nodeUpdateFailed + html.EscapeString(n.Name)
		if f.Error != "" {
			text += ": " + html.EscapeString(f.Error)
		}
		if f.From != "" {
			text += " (" + html.EscapeString(f.From) + " → " + html.EscapeString(f.Version) + ")"
		}
		text += t.nodeUpdateRollout
		st.Pending = appendPending(st.Pending, delivery{Key: "node-update/" + id + "/" + strconv.FormatInt(f.At, 10), Target: targetAdmin, Text: text})
	}
	// A node that was removed, or whose failure is gone, is not remembered.
	for id := range st.NodeUpdateAt {
		found := false
		for _, f := range failures {
			found = found || strconv.FormatInt(f.NodeID, 10) == id
		}
		if !found {
			delete(st.NodeUpdateAt, id)
		}
	}
}

// torrentEvents tells the admin about the torrent blocker's new catches: one message a
// round, so a wave of them does not flood the chat.
func (m *Monitor) torrentEvents(ctx context.Context, st *persistentState, cfg AlertsConfig, lang string) {
	hits, err := m.store.Q.TorrentHitsAfter(ctx, db.TorrentHitsAfterParams{ID: st.TorrentCursor, Limit: torrentFetch})
	if err != nil {
		m.logError("infrastructure alerts: torrent hits", err)
		return
	}
	if len(hits) == 0 {
		return
	}
	st.TorrentCursor = hits[len(hits)-1].ID
	if !cfg.Events.Torrent {
		return
	}
	t := textsFor(lang)
	var b strings.Builder
	b.WriteString(t.torrentTitle)
	for i, h := range hits {
		if i == torrentShown {
			b.WriteString("\n" + fmt.Sprintf(t.torrentMore, len(hits)-torrentShown))
			break
		}
		b.WriteString("\n• <b>" + html.EscapeString(h.UserName) + "</b>: ")
		if h.NodeName != "" {
			b.WriteString(html.EscapeString(h.NodeName) + ", ")
		}
		b.WriteString(html.EscapeString(h.Kind) + " → " + html.EscapeString(h.Dest) + " (" + html.EscapeString(h.Ip) + ")")
		if h.Hits > 1 {
			b.WriteString(" ×" + strconv.Itoa(int(h.Hits)))
		}
		if h.BannedUntil > 0 {
			b.WriteString(", " + fmt.Sprintf(t.torrentBanned, time.Unix(h.BannedUntil, 0).UTC().Format("02.01 15:04")))
		}
	}
	st.Pending = appendPending(st.Pending, delivery{Key: fmt.Sprintf("torrent/%d", st.TorrentCursor), Target: targetAdmin, Text: b.String()})
}
