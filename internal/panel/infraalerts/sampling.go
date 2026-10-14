package infraalerts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/hysteresis"
	"mikan/internal/panel/nodesync"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

// The hysteresis of the samples, in rounds (see hysteresis.Tracker). A node is unavailable after
// hysteresis.NodeFailAfter failed samples; a probe of WARP or an exit makes it degraded after
// probeFailAfter; every kind needs hysteresis.RecoverAfter good samples in a row to be called
// recovered.
const probeFailAfter = 2

// An inbound or the cascade relay is unavailable after inboundFailSamples failed samples. A port
// held by another program is moved by the tuner within seconds: such a listener is reported
// only when the move did not help, after busyFailSamples.
const (
	inboundFailSamples = 3
	busyFailSamples    = 24
)

// worldTTL is how often the round, every five seconds, reads the nodes, the inbounds and the
// nodes' WARP and relay rows: they change only when the admin edits them.
const worldTTL = 15 * time.Second

type world struct {
	at       time.Time
	nodes    []db.Node
	inbounds []db.Inbound
	warp     map[int64]db.NodeWarp
	relay    map[int64]db.NodeRelay
}

// loadWorld returns the nodes and the inbounds, read again once worldTTL has passed.
func (m *Monitor) loadWorld(ctx context.Context) (*world, error) {
	now := m.now()
	if w := m.world; w != nil && now.Sub(w.at) < worldTTL && !now.Before(w.at) {
		return w, nil
	}
	nodes, err := m.store.Q.ListNodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	inbounds, err := m.store.Q.ListInbounds(ctx)
	if err != nil {
		return nil, fmt.Errorf("list inbounds: %w", err)
	}
	m.world = &world{at: now, nodes: nodes, inbounds: inbounds, warp: map[int64]db.NodeWarp{}, relay: map[int64]db.NodeRelay{}}
	return m.world, nil
}

// nodeWarp and nodeRelay read a node's row once per world; an absent row is kept as the
// zero row, an error is not kept.
func (m *Monitor) nodeWarp(ctx context.Context, id int64) (db.NodeWarp, error) {
	if m.world == nil { // outside a round
		return m.store.Q.GetNodeWarp(ctx, id)
	}
	if w, ok := m.world.warp[id]; ok {
		return w, nil
	}
	w, err := m.store.Q.GetNodeWarp(ctx, id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return w, err
	}
	m.world.warp[id] = w
	return w, nil
}

func (m *Monitor) nodeRelay(ctx context.Context, id int64) (db.NodeRelay, error) {
	if m.world == nil {
		return m.store.Q.GetNodeRelay(ctx, id)
	}
	if r, ok := m.world.relay[id]; ok {
		return r, nil
	}
	r, err := m.store.Q.GetNodeRelay(ctx, id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return r, err
	}
	m.world.relay[id] = r
	return r, nil
}

// inboundFailAfter is how many failed samples make an inbound or the cascade relay
// unavailable: busyFailSamples when another program holds the port and the tuner moves it by
// itself, inboundFailSamples otherwise.
func inboundFailAfter(busy, autoPort bool) int {
	if busy && autoPort {
		return busyFailSamples
	}
	return inboundFailSamples
}

func (m *Monitor) round(ctx context.Context) {
	m.roundMu.Lock()
	dispatch := false
	defer func() {
		m.roundMu.Unlock()
		if dispatch {
			m.startDelivery(ctx)
		}
	}()
	st, err := m.load(ctx)
	if err != nil {
		m.logError("infrastructure alerts: load state", err)
		return
	}
	cfg := m.config(ctx)
	lang := "ru"
	if m.bot != nil {
		lang = m.bot.PolledLang(ctx)
	}
	t := textsFor(lang)
	w, err := m.loadWorld(ctx)
	if err != nil {
		m.logError("infrastructure alerts", err)
		return
	}
	nodes, inbounds := w.nodes, w.inbounds
	byNode := map[int64][]db.Inbound{}
	for _, in := range inbounds {
		byNode[in.NodeID] = append(byNode[in.NodeID], in)
	}
	levels := make(map[int64]hysteresis.Level, len(nodes))
	listenerHealth := make(map[int64]map[string]bool, len(nodes))
	// Whether the tuner moves a busy relay by itself: there is no switch for one relay.
	relayMoves := false
	if m.settings != nil {
		on, err := m.settings.On(ctx, settings.AutoPort)
		if err != nil {
			m.logError("infrastructure alerts: settings", err)
		}
		relayMoves = on && err == nil
	}
	for _, n := range nodes {
		if n.Enabled == 0 {
			continue
		}
		hv, ok := nodesync.HealthView{}, false
		if m.runtime != nil {
			hv, ok = m.runtime.Health(n.ID)
		}
		if !ok || hv.CheckedAt.IsZero() {
			continue
		}
		nodeLevel := hysteresis.Healthy
		if !hv.OK {
			nodeLevel = hysteresis.Unavailable
		}
		m.observe(&st, "node/"+strconv.FormatInt(n.ID, 10), nodeLevel, hv.CheckedAt, hysteresis.NodeFailAfter, hysteresis.RecoverAfter, eventFor(cfg, eventNode, n.Name, nodeLevel, t))
		if nodeLevel == hysteresis.Unavailable {
			levels[n.ID] = hysteresis.Unavailable
			continue
		}
		listenerHealth[n.ID] = make(map[string]bool, len(hv.Listeners))
		busy := map[string]bool{}
		for _, l := range hv.Listeners {
			listenerHealth[n.ID][l.Name] = l.OK
			busy[l.Name] = l.Busy()
		}
		for _, in := range byNode[n.ID] {
			if in.Enabled == 0 {
				continue
			}
			ok := listenerHealth[n.ID][in.Name]
			level := hysteresis.Healthy
			if !ok {
				level = hysteresis.Unavailable
			}
			key := "inbound/" + strconv.FormatInt(in.ID, 10)
			m.observe(&st, key, level, hv.CheckedAt, inboundFailAfter(busy[in.Name], in.AutoPort != 0), hysteresis.RecoverAfter, eventFor(cfg, eventInbound, n.Name+" / "+in.Name, level, t))
			if level != hysteresis.Healthy {
				nodeLevel = hysteresis.Degraded
			}
		}
		if m.checkRelay(&st, cfg, n, hv, relayMoves, lang) != hysteresis.Healthy {
			nodeLevel = hysteresis.Degraded
		}
		levels[n.ID] = nodeLevel
	}

	// The API calls are cached by each node for one minute. Count only a new probe result,
	// not every panel round that reads the same cached response.
	if m.runtime != nil {
		m.probeExits(ctx, &st, cfg, nodes, byNode, levels, lang)
	}
	if m.runtime != nil {
		m.probeWarp(ctx, &st, cfg, nodes, byNode, levels, lang)
	}
	nodeByID := make(map[int64]db.Node, len(nodes))
	for _, n := range nodes {
		nodeByID[n.ID] = n
	}
	inboundByID := make(map[int64]db.Inbound, len(inbounds))
	for _, in := range inbounds {
		inboundByID[in.ID] = in
	}
	m.autotuneEvents(ctx, &st, cfg, nodeByID, inboundByID, lang)
	m.torrentEvents(ctx, &st, cfg, lang)
	m.checkTuner(&st, cfg, byNode, nodeByID, lang)
	m.checkCertificate(&st, cfg, lang)
	m.checkUpdate(&st, cfg, lang)
	m.checkNodeUpdates(ctx, &st, cfg, nodeByID, lang)
	m.publicStatus(ctx, &st, cfg, nodes, byNode, lang)
	forgetRemoved(&st, nodes, byNode)
	enabled := m.bot != nil && m.bot.PolledInfrastructure(ctx) // read once per round
	if !enabled {
		st.Pending = nil
	}
	if err := m.save(ctx, st); err != nil {
		m.logError("infrastructure alerts: save state", err)
	} else if enabled && len(st.Pending) > 0 {
		dispatch = true
	}
}

// forgetRemoved drops the samples and the stuck marks of nodes and inbounds that are gone.
// Samples only matter for configured infrastructure: without this the JSON snapshot would
// grow as installations change over time.
func forgetRemoved(st *persistentState, nodes []db.Node, byNode map[int64][]db.Inbound) {
	valid := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		valid["node/"+strconv.FormatInt(n.ID, 10)] = true
		valid["warp/"+strconv.FormatInt(n.ID, 10)] = true
		valid["relay/"+strconv.FormatInt(n.ID, 10)] = true
	}
	for _, list := range byNode {
		for _, in := range list {
			valid["inbound/"+strconv.FormatInt(in.ID, 10)] = true
		}
	}
	for key := range st.Samples {
		if strings.HasPrefix(key, "node/") || strings.HasPrefix(key, "warp/") || strings.HasPrefix(key, "relay/") || strings.HasPrefix(key, "inbound/") {
			if !valid[key] {
				delete(st.Samples, key)
			}
		}
	}
	for key := range st.Stuck {
		found := false
		for nodeID, list := range byNode {
			for _, in := range list {
				if key == fmt.Sprintf("%d/%d", nodeID, in.ID) {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			delete(st.Stuck, key)
		}
	}
}

// observe records a new sample, with a stable failure threshold and two-sample recovery.
// A false return means no stable transition was emitted.
func (m *Monitor) observe(st *persistentState, key string, level hysteresis.Level, checked time.Time, failAfter, recoverSamples int, text string) bool {
	s := st.Samples[key]
	if !s.Checked.IsZero() && !checked.After(s.Checked) {
		return false
	}
	s.Checked = checked
	tr := s.Tracker.Observe(level, failAfter, recoverSamples)
	st.Samples[key] = s
	if tr == nil || text == "" {
		return tr != nil
	}
	st.Pending = appendPending(st.Pending, delivery{Key: key + "/" + checked.UTC().Format(time.RFC3339Nano), Target: targetAdmin, Text: text})
	return true
}

// eventKind is a kind of sampled event; the config switches them one by one (Events).
type eventKind string

const (
	eventNode    eventKind = "node"
	eventWarp    eventKind = "warp"
	eventExit    eventKind = "exit"
	eventInbound eventKind = "inbound"
)

func eventFor(c AlertsConfig, event eventKind, name string, level hysteresis.Level, t *texts) string {
	var enabled bool
	switch event {
	case eventNode:
		enabled = c.Events.Node
	case eventWarp:
		enabled = c.Events.Warp
	case eventExit:
		enabled = c.Events.Exit
	case eventInbound:
		enabled = c.Events.Inbound
	}
	if !enabled {
		return ""
	}
	icon, state := "🔴", t.stateUnavailable
	if level == hysteresis.Healthy {
		icon, state = "🟢", t.stateRecovered
	}
	if level == hysteresis.Degraded {
		icon, state = "🟡", t.stateDegraded
	}
	return icon + " <b>" + html.EscapeString(name) + "</b> — " + state
}

// checkRelay watches the cascade relay's listener: other nodes leave through it, and a port
// another program holds keeps it down without a word. A node without a relay has no such
// listener and is Healthy here. moves says whether the tuner moves a busy relay by itself,
// which is given time to, as an inbound's move is.
func (m *Monitor) checkRelay(st *persistentState, cfg AlertsConfig, n db.Node, hv nodesync.HealthView, moves bool, lang string) hysteresis.Level {
	i := slices.IndexFunc(hv.Listeners, func(l nodeapi.ListenerStatus) bool { return l.Name == nodeapi.RelayListener })
	if i < 0 {
		return hysteresis.Healthy
	}
	l, level := hv.Listeners[i], hysteresis.Healthy
	if !l.OK {
		level = hysteresis.Unavailable
	}
	t := textsFor(lang)
	label := n.Name + " / " + t.relayLabel
	m.observe(st, "relay/"+strconv.FormatInt(n.ID, 10), level, hv.CheckedAt, inboundFailAfter(l.Busy(), moves), hysteresis.RecoverAfter, eventFor(cfg, eventExit, label, level, t))
	return level
}

// warpReason says in a line why the node's WARP check failed, so the alert is not just
// "down". Codes come from the node (nodeapi.WarpStatus); the node's own detail is
// English only, so it is the fallback for codes this panel does not know.
func warpReason(s nodeapi.WarpStatus, endpoint, lang string) string {
	t := textsFor(lang)
	var text string
	switch s.Error {
	case "timeout":
		text = fmt.Sprintf(t.warpTimeout, endpoint)
	case "https_timeout":
		text = t.warpHTTPSTimeout
	case "dns":
		text = t.warpDNS
	case "tls":
		text = t.warpTLS
	case "refused":
		text = t.warpRefused
	case "not_loaded":
		text = t.warpNotLoaded
	case "bad_answer":
		text = t.warpBadAnswer
	default:
		text = s.Detail
		if text == "" {
			text = s.Error
		}
		if text == "" {
			text = t.warpCheckFailed
		}
	}
	return t.warpReason + text
}

func (m *Monitor) probeWarp(ctx context.Context, st *persistentState, cfg AlertsConfig, nodes []db.Node, inbounds map[int64][]db.Inbound, levels map[int64]hysteresis.Level, lang string) {
	type ask struct {
		n db.Node
		w db.NodeWarp
	}
	var asks []ask
	for _, n := range nodes {
		if n.Enabled == 0 || levels[n.ID] == hysteresis.Unavailable {
			continue
		}
		w, err := m.nodeWarp(ctx, n.ID)
		if err != nil || w.Enabled == 0 {
			continue
		}
		asks = append(asks, ask{n, w})
	}
	type answer struct {
		s   nodeapi.WarpStatus
		err error
	}
	answers := askAll(len(asks), func(i int) answer {
		s, err := m.runtime.Warp(ctx, asks[i].n.ID, false)
		return answer{s, err}
	})
	t := textsFor(lang)
	for i, a := range asks {
		s, err := answers[i].s, answers[i].err
		if err != nil || s.CheckedAt.IsZero() {
			continue
		}
		level := hysteresis.Healthy
		if !s.OK {
			level = hysteresis.Degraded
		}
		text := eventFor(cfg, eventWarp, a.n.Name+" / WARP", level, t)
		if text != "" && level == hysteresis.Degraded {
			text += "\n" + html.EscapeString(warpReason(s, a.w.Endpoint, lang))
		}
		m.observe(st, "warp/"+strconv.FormatInt(a.n.ID, 10), level, s.CheckedAt, probeFailAfter, hysteresis.RecoverAfter, text)
		if level != hysteresis.Healthy {
			levels[a.n.ID] = hysteresis.Degraded
		}
	}
}

func (m *Monitor) probeExits(ctx context.Context, st *persistentState, cfg AlertsConfig, nodes []db.Node, inbounds map[int64][]db.Inbound, levels map[int64]hysteresis.Level, lang string) {
	name := map[int64]string{}
	for _, n := range nodes {
		name[n.ID] = n.Name
	}
	type ask struct {
		node, exit int64
		key        string
	}
	var asks []ask
	seen := map[string]bool{}
	for _, n := range nodes {
		if n.Enabled == 0 || levels[n.ID] == hysteresis.Unavailable {
			continue
		}
		exits := map[int64]bool{}
		for _, in := range inbounds[n.ID] {
			if in.Enabled != 0 && in.Outbound == "node" && in.ExitNodeID.Valid {
				exits[in.ExitNodeID.Int64] = true
			}
		}
		relay, err := m.nodeRelay(ctx, n.ID)
		if err == nil && relay.Outbound == "node" && relay.ExitNodeID.Valid {
			exits[relay.ExitNodeID.Int64] = true
		}
		for exitID := range exits {
			key := fmt.Sprintf("exit/%d/%d", n.ID, exitID)
			if seen[key] {
				continue
			}
			seen[key] = true
			asks = append(asks, ask{n.ID, exitID, key})
		}
	}
	type answer struct {
		p   nodeapi.ProbeResult
		err error
	}
	answers := askAll(len(asks), func(i int) answer {
		p, err := m.runtime.Probe(ctx, asks[i].node, nodeapi.ExitName(asks[i].exit))
		return answer{p, err}
	})
	t := textsFor(lang)
	for i, a := range asks {
		p, err := answers[i].p, answers[i].err
		if err != nil || p.CheckedAt.IsZero() {
			continue
		}
		level := hysteresis.Healthy
		if !p.OK {
			level = hysteresis.Degraded
		}
		label := name[a.node] + " → " + name[a.exit]
		m.observe(st, a.key, level, p.CheckedAt, probeFailAfter, hysteresis.RecoverAfter, eventFor(cfg, eventExit, label, level, t))
		if level != hysteresis.Healthy {
			levels[a.node] = hysteresis.Degraded
		}
	}
}

// askAll asks n questions at once and returns the answers in order. The nodes' checks
// (WARP, the exits) may take up to the client's timeout each: asked one after another, one
// stuck check held the five-second round, and every node's health with it, for each node
// in turn.
func askAll[T any](n int, ask func(i int) T) []T {
	out := make([]T, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() { out[i] = ask(i) })
	}
	wg.Wait()
	return out
}
