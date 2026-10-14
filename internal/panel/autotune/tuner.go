package autotune

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/audit"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/nodesync"
	"mikan/internal/panel/presets"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/proto"
	"mikan/internal/scan"
)

// Nodes is what the tuner needs from the running nodes (nodesync.Manager).
type Nodes interface {
	Activity(ctx context.Context, id int64) (nodeapi.Activity, error)
	CheckTarget(ctx context.Context, id int64, req nodeapi.TargetCheckRequest) (nodeapi.TargetResult, error)
	ScanTargets(ctx context.Context, id int64, req nodeapi.TargetScanRequest) (nodeapi.TargetScan, error)
	Health(id int64) (nodesync.HealthView, bool)
}

// Options are the tuner's timings. The windows are long on purpose: a server whose
// traffic the DPI drops at random (every port loses some connections) must not look like
// a server with one blocked port.
type Options struct {
	Tick        time.Duration // detector round
	Window      time.Duration // how far back device activity counts
	Hold        time.Duration // an inbound stays cut off this long before anything changes
	Cooldown    time.Duration // no second change of the same kind of one inbound before this
	Escalate    time.Duration // a port move that did not help: another target after this
	CheckEvery  time.Duration // REALITY target checks
	TargetFails int           // failed target checks in a row before the target is replaced
	// At most MaxChanges automatic changes of one inbound per ChangesWindow: a protocol the
	// DPI recognizes by its traffic is not fixed by ports and targets.
	MaxChanges    int
	ChangesWindow time.Duration
	Abandon       time.Duration // a port given up on a node is not picked again for this long
	Busy          time.Duration // the nodes' listeners are looked at for a port another program holds
}

func DefaultOptions() Options {
	return Options{Tick: 5 * time.Minute, Window: 30 * time.Minute, Hold: 30 * time.Minute, Cooldown: 6 * time.Hour,
		Escalate: time.Hour, CheckEvery: 15 * time.Minute, TargetFails: 3, MaxChanges: 3, ChangesWindow: 48 * time.Hour,
		Abandon: 7 * 24 * time.Hour, Busy: 15 * time.Second}
}

// Scaled shortens every timing by f, for tests on a real stack.
func (o Options) Scaled(f float64) Options {
	s := func(d *time.Duration) { *d = time.Duration(float64(*d) * f) }
	for _, d := range []*time.Duration{&o.Tick, &o.Window, &o.Hold, &o.Cooldown, &o.Escalate, &o.CheckEvery, &o.ChangesWindow, &o.Abandon, &o.Busy} {
		s(d)
	}
	return o
}

// Status is the tuner's view of one inbound, for the admin.
type Status struct {
	CutOff  bool      // devices that reach the node's other inbounds do not reach this one
	Since   time.Time // cut off since
	Blocked int       // devices cut off from it
	Reached int       // devices that reached it (of those that try every inbound)
	// Stuck says why a cut-off inbound stays as it is: off (switched off), waiting (a
	// change was made, clients are catching up), no_port, no_target, exhausted.
	Stuck string
	// The REALITY target's last check; TargetAt is zero before the first one.
	TargetOK    bool
	TargetError string
	TargetAt    time.Time
}

// Tuner detects blocked inbounds and moves them. Rounds are serialized (stepMu): Run and
// anything that asks for one wait for each other.
type Tuner struct {
	st       *store.Store
	inbounds *domain.Inbounds // moves ports by the admin's rules, without a dry run on the node
	set      *settings.Settings
	nodes    Nodes
	changes  domain.Changes
	log      *slog.Logger
	now      func() time.Time
	o        Options
	pick     func(n int) int // index of the port to move to; random unless a test fixes it

	stepMu sync.Mutex    // one round at a time
	round  time.Duration // overrides roundTimeout; tests only

	mu    sync.Mutex
	state map[int64]*state // by inbound id
	busy  map[int64]string // by inbound id (a relay: minus the node's id): why a busy listener stays, as last logged
	// relayLeft are the ports each node's relay left lately, by node id: there is no event
	// table for them as there is for inbounds, so a restart forgets them.
	relayLeft map[int64]map[string]time.Time

	busyMu sync.Mutex // one MoveBusy at a time

	// What only a round touches: its own timings, and the targets that failed a check lately
	// (not picked again). The target checks of a round run side by side and note failures
	// under mu; a replacement reads them once the checks are over.
	failed    map[string]time.Time
	checkedAt time.Time
	prunedAt  time.Time
}

type state struct {
	Status
	fails int // target checks failed in a row
}

func New(st *store.Store, set *settings.Settings, nodes Nodes, changes domain.Changes, log *slog.Logger, now func() time.Time, o Options) *Tuner {
	return &Tuner{st: st, inbounds: domain.NewInbounds(st, nil, now), set: set, nodes: nodes, changes: changes, log: log, now: now, o: o, pick: rand.IntN,
		state: map[int64]*state{}, busy: map[int64]string{}, relayLeft: map[int64]map[string]time.Time{}, failed: map[string]time.Time{}}
}

// Status returns the tuner's view of an inbound; false before its first round.
func (t *Tuner) Status(id int64) (Status, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.state[id]
	if !ok {
		return Status{}, false
	}
	return s.Status, true
}

func (t *Tuner) Run(ctx context.Context) {
	if t.o.Busy > 0 {
		var wg sync.WaitGroup
		defer wg.Wait()
		wg.Go(func() { t.runBusy(ctx) })
	}
	tick := time.NewTicker(t.o.Tick)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			t.Step(ctx)
		}
	}
}

// world is the database as one round sees it; changes made in the round update it.
type world struct {
	now          time.Time
	portOn       bool
	anyPort      bool // settings.AnyPort: moves may go past domain.PortPool
	sniOn        bool
	nodes        []db.Node              // enabled nodes
	inbounds     map[int64][]db.Inbound // by node id, enabled or not
	users        map[string]User        // by slot name: the users' own keys and their devices'
	reach        map[string]map[int64]int64
	events       []db.InboundEvent
	ownNames     map[string]bool // the panel's and nodes' own names: never a REALITY target
	panelHost    string
	panelPort    int
	eventsWindow time.Duration
}

// roundTimeout is the most a round may take: a node that does not answer holds a check for
// 15 seconds and a scan for 45, and a round must end before the next tick for the
// detector to run on time.
func (t *Tuner) roundTimeout() time.Duration {
	if t.round > 0 {
		return t.round
	}
	return max(t.o.Tick, 2*time.Minute)
}

// Step runs a detector round over all nodes and, when due, a target round.
func (t *Tuner) Step(ctx context.Context) {
	t.stepMu.Lock()
	defer t.stepMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, t.roundTimeout())
	defer cancel()
	w, err := t.load(ctx)
	if err != nil {
		t.log.Error("autotune: load", "err", err)
		return
	}
	t.forget(w)
	if w.now.Sub(t.checkedAt) >= t.o.CheckEvery {
		t.checkedAt = w.now
		t.checkTargets(ctx, w)
	}
	for _, n := range w.nodes {
		t.detect(ctx, w, n)
	}
	if w.now.Sub(t.prunedAt) >= 24*time.Hour {
		t.prunedAt = w.now
		if err := t.st.Q.PruneInboundEvents(ctx, w.now.Add(-30*24*time.Hour).Unix()); err != nil {
			t.log.Error("autotune: prune events", "err", err)
		}
		if err := t.st.Q.PruneSubFetches(ctx, w.now.Add(-fetchesKeep).Unix()); err != nil {
			t.log.Error("autotune: prune subscription fetches", "err", err)
		}
		if err := t.st.Q.PruneInboundReach(ctx, w.now.Add(-reachKeep).Unix()); err != nil {
			t.log.Error("autotune: prune reach", "err", err)
		}
		t.mu.Lock()
		for dest, at := range t.failed {
			if w.now.Sub(at) >= 24*time.Hour {
				delete(t.failed, dest)
			}
		}
		t.mu.Unlock()
	}
}

// fetchesKeep: a device that has not taken its profile for this long is gone or has
// auto-update off; either way its silence proves nothing.
const fetchesKeep = 30 * 24 * time.Hour

// reachKeep: how long the tuner remembers that a device got through to an inbound. A port
// blocked for longer than that has long been moved or given up on.
const reachKeep = 30 * 24 * time.Hour

// reachStep: a newer reach of the same inbound is written down only this much later; the
// detector needs "reached before", not the exact time.
const reachStep = time.Hour

func (t *Tuner) load(ctx context.Context) (*world, error) {
	w := &world{now: t.now(), inbounds: map[int64][]db.Inbound{}, users: map[string]User{}, ownNames: map[string]bool{},
		eventsWindow: max(t.o.ChangesWindow, t.o.Abandon, t.o.Cooldown)}
	q := t.st.Q
	var err error
	if w.portOn, err = t.set.On(ctx, settings.AutoPort); err != nil {
		return nil, err
	}
	if w.anyPort, err = t.set.On(ctx, settings.AnyPort); err != nil {
		return nil, err
	}
	if w.sniOn, err = t.set.On(ctx, settings.AutoSNI); err != nil {
		return nil, err
	}
	own := func(name string) {
		if name = strings.ToLower(strings.TrimSpace(name)); name != "" {
			w.ownNames[name] = true
		}
	}
	nodes, err := q.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	for _, n := range nodes {
		own(n.Domain)
		own(n.PublicHost)
		if n.Enabled != 0 {
			w.nodes = append(w.nodes, n)
		}
	}
	inbounds, err := q.ListInbounds(ctx)
	if err != nil {
		return nil, err
	}
	for _, in := range inbounds {
		w.inbounds[in.NodeID] = append(w.inbounds[in.NodeID], in)
	}
	slots, err := q.ListSlots(ctx)
	if err != nil {
		return nil, err
	}
	slotName := make(map[int64]string, len(slots))
	for _, s := range slots {
		slotName[s.ID] = s.Name
	}
	fetches, err := q.ListSubFetchesSince(ctx, w.now.Add(-fetchesKeep).Unix())
	if err != nil {
		return nil, err
	}
	fetched := map[int64]map[string]int64{}
	for _, f := range fetches {
		if fetched[f.UserID] == nil {
			fetched[f.UserID] = map[string]int64{}
		}
		fetched[f.UserID][f.Ip] = f.FetchedAt
	}
	users, err := q.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	allowed := make(map[int64][]int64, len(users))
	for _, u := range users {
		allowed[u.ID] = domain.DecodeInbounds(u.Inbounds)
		if u.SlotID.Valid {
			w.users[slotName[u.SlotID.Int64]] = User{Inbounds: allowed[u.ID], SubFetched: fetched[u.ID]}
		}
	}
	// Bound devices use keys of their own: the same profile as their user's, fetched on
	// their own schedule.
	devices, err := q.ListDeviceSlots(ctx)
	if err != nil {
		return nil, err
	}
	for _, d := range devices {
		w.users[d.SlotName] = User{Inbounds: allowed[d.UserID], Fetched: d.LastSeen}
	}
	reach, err := q.ListInboundReachSince(ctx, w.now.Add(-reachKeep).Unix())
	if err != nil {
		return nil, err
	}
	w.reach = make(map[string]map[int64]int64)
	for _, r := range reach {
		if w.reach[r.Slot] == nil {
			w.reach[r.Slot] = map[int64]int64{}
		}
		w.reach[r.Slot][r.InboundID] = r.At
	}
	if w.events, err = q.ListInboundEventsSince(ctx, w.now.Add(-w.eventsWindow).Unix()); err != nil {
		return nil, err
	}
	var panelDomain string
	if panelDomain, err = t.set.String(ctx, settings.KeyDomain); err != nil {
		return nil, err
	}
	if w.panelHost, err = t.set.String(ctx, settings.KeyPublicHost); err != nil {
		return nil, err
	}
	own(panelDomain)
	own(w.panelHost)
	if w.panelPort, _, err = settings.Get[int](ctx, t.set, settings.KeyPanelPort); err != nil {
		return nil, err
	}
	return w, nil
}

// forget drops what the tuner knew about inbounds that are gone or switched off.
func (t *Tuner) forget(w *world) {
	live := map[int64]bool{}
	for _, n := range w.nodes {
		for _, in := range w.inbounds[n.ID] {
			if in.Enabled != 0 {
				live[in.ID] = true
			}
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for id := range t.state {
		if !live[id] {
			delete(t.state, id)
		}
	}
}

func (t *Tuner) stateLocked(id int64) *state {
	s := t.state[id]
	if s == nil {
		s = &state{}
		t.state[id] = s
	}
	return s
}

func (t *Tuner) setStuck(id int64, why string) {
	t.mu.Lock()
	t.stateLocked(id).Stuck = why
	t.mu.Unlock()
}

func (t *Tuner) detect(ctx context.Context, w *world, n db.Node) {
	var enabled []db.Inbound
	for _, in := range w.inbounds[n.ID] {
		if in.Enabled != 0 {
			enabled = append(enabled, in)
		}
	}
	if len(enabled) == 0 {
		return
	}
	act, err := t.nodes.Activity(ctx, n.ID)
	if err != nil {
		// A node older than 0.3 has no activity; an unreachable node has nothing to judge.
		t.log.Debug("autotune: activity", "node", n.ID, "err", err)
		return
	}
	t.recordReach(ctx, w, enabled, act)
	verdicts := Detect(Evidence{Inbounds: enabled, Users: w.users, Activity: act, Reach: w.reach, Since: w.now.Add(-t.o.Window)})
	for _, x := range enabled {
		v := verdicts[x.ID]
		t.mu.Lock()
		s := t.stateLocked(x.ID)
		s.Blocked, s.Reached = v.Blocked, v.Reached
		if !v.CutOff() {
			s.CutOff, s.Since, s.Stuck = false, time.Time{}, ""
			t.mu.Unlock()
			continue
		}
		if !s.CutOff {
			s.CutOff, s.Since = true, w.now
		}
		due := w.now.Sub(s.Since) >= t.o.Hold
		t.mu.Unlock()
		if due {
			t.remedy(ctx, w, n, x)
		}
	}
}

// recordReach writes down which inbounds the node's known devices got through to, for the
// detector's "reached before".
func (t *Tuner) recordReach(ctx context.Context, w *world, inbounds []db.Inbound, act nodeapi.Activity) {
	ids := make(map[string]int64, len(inbounds))
	for _, in := range inbounds {
		ids[in.Name] = in.ID
	}
	// What is new enough to write down, each (slot, inbound) once with its latest time.
	type key struct {
		slot string
		id   int64
	}
	due := map[key]int64{}
	for _, c := range act.Clients {
		if _, known := w.users[c.Slot]; !known {
			continue
		}
		for name, at := range c.Seen {
			id, ok := ids[name]
			if !ok || at-w.reach[c.Slot][id] < int64(reachStep/time.Second) {
				continue
			}
			k := key{c.Slot, id}
			due[k] = max(due[k], at)
		}
	}
	if len(due) == 0 {
		return
	}
	p := db.UpsertInboundReachesParams{}
	for k, at := range due {
		p.Slots, p.InboundIds, p.Ats = append(p.Slots, k.slot), append(p.InboundIds, k.id), append(p.Ats, at)
	}
	// One blind upsert for the whole node: READ COMMITTED.
	err := t.st.TxRC(ctx, func(q *db.Queries) error { return q.UpsertInboundReaches(ctx, p) })
	if err != nil {
		t.log.Error("autotune: record reach", "err", err)
		return
	}
	// The cache only after the commit: a retry must still write every reach.
	for k, at := range due {
		if w.reach[k.slot] == nil {
			w.reach[k.slot] = map[int64]int64{}
		}
		w.reach[k.slot][k.id] = at
	}
}

// history of an inbound's automatic changes in the round's event window.
type history struct {
	lastPort, lastSNI time.Time
	recent            int // changes within ChangesWindow
}

func (t *Tuner) history(w *world, id int64) history {
	var h history
	for _, e := range w.events {
		if e.InboundID != id {
			continue
		}
		at := time.Unix(e.CreatedAt, 0)
		switch e.Kind {
		case "port":
			h.lastPort = at
		case "sni":
			h.lastSNI = at
		}
		// A move off a port another program held gives clients the same time to catch up,
		// but it is no sign of a blocked protocol: it does not count against MaxChanges.
		if w.now.Sub(at) < t.o.ChangesWindow && e.Reason != ReasonBusy {
			h.recent++
		}
	}
	return h
}

// remedy tries the next step for an inbound that stays cut off: a new target when its
// REALITY target is down, else a new port, else — the port did not help — a new target.
func (t *Tuner) remedy(ctx context.Context, w *world, n db.Node, x db.Inbound) {
	tpl, err := proto.Parse(x.Config)
	if err != nil {
		return
	}
	dest, _ := presets.Dest(tpl)
	// Hopping ranges are the admin's, and so are the port and the site a proxy in front
	// forwards by.
	portOK := w.portOn && x.AutoPort != 0 && !strings.Contains(x.Port, "-") && !domain.ListenPinsPort(x.Listen)
	sniOK := w.sniOn && x.AutoSni != 0 && dest != "" && !domain.ListenPinsSNI(x.Listen)
	h := t.history(w, x.ID)
	switch {
	case !portOK && !sniOK:
		t.setStuck(x.ID, "off")
		return
	case h.recent >= t.o.MaxChanges:
		t.setStuck(x.ID, "exhausted")
		return
	}
	portFree := w.now.Sub(h.lastPort) >= t.o.Cooldown
	sniFree := w.now.Sub(h.lastSNI) >= t.o.Cooldown
	if sniOK && sniFree {
		// Cut off and the target fails: one failed check is enough here. Without another
		// target, a new port may still help.
		if r, err := t.checkTarget(ctx, n.ID, x, tpl, false); err == nil && !r.OK && t.replaceTarget(ctx, w, n, x, tpl, "target_down") {
			return
		}
	}
	if portOK && portFree {
		t.movePort(ctx, w, n, x, "blocked")
		return
	}
	if sniOK && sniFree && (!portOK || !h.lastPort.IsZero() && w.now.Sub(h.lastPort) >= t.o.Escalate) {
		reason := "still_blocked"
		if h.lastPort.IsZero() {
			reason = "blocked"
		}
		t.replaceTarget(ctx, w, n, x, tpl, reason)
		return
	}
	if !portFree || !sniFree {
		t.setStuck(x.ID, "waiting")
		return
	}
	t.setStuck(x.ID, "exhausted")
}

// nodePorts is node's port map with what its server listens on, as the node last said.
func (t *Tuner) nodePorts(ctx context.Context, n db.Node) (domain.PortMap, error) {
	ports, err := domain.NodePorts(ctx, t.st.Q, n)
	if err != nil {
		return ports, err
	}
	if hv, ok := t.nodes.Health(n.ID); ok {
		ports = ports.WithHost(hv.HostPorts())
	}
	return ports, nil
}

func (t *Tuner) movePort(ctx context.Context, w *world, n db.Node, x db.Inbound, reason string) {
	network := domain.InboundNetwork(x)
	ports, err := t.nodePorts(ctx, n)
	if err != nil {
		t.log.Error("autotune: node ports", "node", n.ID, "err", err)
		return
	}
	abandoned := t.leftPorts(w.events, n.ID, network, w.now)
	abandoned[x.Port] = true
	free := FreePorts(ports, network, abandoned, w.anyPort)
	if len(free) == 0 {
		t.setStuck(x.ID, "no_port")
		return
	}
	port := free[t.pick(len(free))]
	prev, next, err := t.inbounds.Update(ctx, x.ID, domain.InboundPatch{Port: &port})
	if err != nil {
		t.log.Error("autotune: move port", "node", n.ID, "inbound", x.Name, "err", err)
		return
	}
	t.changed(ctx, w, n, next, db.AddInboundEventParams{Kind: "port", Network: network, OldValue: prev.Port, NewValue: next.Port, Reason: reason})
}

// checkTarget tests an inbound's REALITY target on its node; a check younger than
// CheckEvery is reused unless fresh is set.
func (t *Tuner) checkTarget(ctx context.Context, nodeID int64, x db.Inbound, tpl proto.Template, fresh bool) (nodeapi.TargetResult, error) {
	dest, names := presets.Dest(tpl)
	t.mu.Lock()
	s := t.stateLocked(x.ID)
	cached := !fresh && !s.TargetAt.IsZero() && t.now().Sub(s.TargetAt) < t.o.CheckEvery
	last := nodeapi.TargetResult{OK: s.TargetOK, Error: s.TargetError}
	t.mu.Unlock()
	if cached {
		return last, nil
	}
	req := nodeapi.TargetCheckRequest{Dest: dest}
	if len(names) > 0 {
		req.SNI = names[0]
	}
	r, err := t.nodes.CheckTarget(ctx, nodeID, req)
	if err != nil {
		t.log.Debug("autotune: check target", "node", nodeID, "inbound", x.Name, "err", err)
		return r, err
	}
	now := t.now()
	t.mu.Lock()
	s = t.stateLocked(x.ID)
	s.TargetOK, s.TargetError, s.TargetAt = r.OK, scan.Problem(r), now
	if r.OK {
		s.fails = 0
	} else {
		s.fails++
		t.failed[strings.ToLower(dest)] = now
	}
	t.mu.Unlock()
	return r, nil
}

// checkTargets checks every REALITY target and replaces one that failed TargetFails
// checks in a row. Targets are checked even with replacement off: the admin sees them.
// The nodes are asked side by side, one goroutine each (a node that does not answer holds
// only its own checks), and a site shared by several inbounds of a node is asked about
// once; the replacements, which read what the checks found, follow in the nodes' order.
func (t *Tuner) checkTargets(ctx context.Context, w *world) {
	type candidate struct {
		n   db.Node
		x   db.Inbound
		tpl proto.Template
	}
	found := make([][]candidate, len(w.nodes))
	var wg sync.WaitGroup
	for i, n := range w.nodes {
		wg.Go(func() {
			seen := map[string]*nodeapi.TargetResult{} // dest/sni → the answer, nil: it failed to be asked
			for _, x := range w.inbounds[n.ID] {
				if x.Enabled == 0 || ctx.Err() != nil {
					continue
				}
				tpl, err := proto.Parse(x.Config)
				if err != nil {
					continue
				}
				dest, names := presets.Dest(tpl)
				if dest == "" {
					continue
				}
				key := strings.ToLower(dest)
				if len(names) > 0 {
					key += "/" + strings.ToLower(names[0])
				}
				if prev, ok := seen[key]; ok {
					if prev != nil {
						t.noteTarget(x.ID, dest, *prev)
						if !prev.OK {
							found[i] = append(found[i], candidate{n, x, tpl})
						}
					}
					continue
				}
				r, err := t.checkTarget(ctx, n.ID, x, tpl, true)
				if err != nil {
					seen[key] = nil
					continue
				}
				seen[key] = &r
				if !r.OK {
					found[i] = append(found[i], candidate{n, x, tpl})
				}
			}
		})
	}
	wg.Wait()
	for _, list := range found {
		for _, c := range list {
			if ctx.Err() != nil {
				return
			}
			t.mu.Lock()
			fails := t.stateLocked(c.x.ID).fails
			t.mu.Unlock()
			h := t.history(w, c.x.ID)
			if fails >= t.o.TargetFails && w.sniOn && c.x.AutoSni != 0 && !domain.ListenPinsSNI(c.x.Listen) && w.now.Sub(h.lastSNI) >= t.o.Cooldown && h.recent < t.o.MaxChanges {
				t.replaceTarget(ctx, w, c.n, c.x, c.tpl, "target_down")
			}
		}
	}
}

// noteTarget records, for an inbound that shares a site with one already checked, the
// answer of that check.
func (t *Tuner) noteTarget(inbound int64, dest string, r nodeapi.TargetResult) {
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.stateLocked(inbound)
	s.TargetOK, s.TargetError, s.TargetAt = r.OK, scan.Problem(r), now
	if r.OK {
		s.fails = 0
	} else {
		s.fails++
		t.failed[strings.ToLower(dest)] = now
	}
}

// replaceTarget points a REALITY inbound at the best working site next to its node:
// never the panel's or a node's own name, never a target that failed lately, preferably
// one the node's other inbounds do not use. It reports whether the target changed.
func (t *Tuner) replaceTarget(ctx context.Context, w *world, n db.Node, x db.Inbound, tpl proto.Template, reason string) bool {
	ip, err := t.nodeIP(ctx, w, n)
	if err != nil {
		t.setStuck(x.ID, "no_target")
		return false
	}
	wide, err := t.set.On(ctx, settings.WideScan)
	if err != nil {
		t.log.Warn("autotune: scan targets: settings", "node", n.ID, "err", err)
		return false
	}
	limit := 16
	if wide {
		limit = scan.WideEnough
	}
	found, err := t.nodes.ScanTargets(ctx, n.ID, nodeapi.TargetScanRequest{IP: ip, Limit: limit, Wide: wide})
	if err != nil {
		t.log.Warn("autotune: scan targets", "node", n.ID, "err", err)
		return false
	}
	oldDest, oldNames := presets.Dest(tpl)
	used := map[string]bool{}
	for _, in := range w.inbounds[n.ID] {
		if in.ID == x.ID || in.Enabled == 0 {
			continue
		}
		if other, err := proto.Parse(in.Config); err == nil {
			_, names := presets.Dest(other)
			for _, name := range names {
				used[strings.ToLower(name)] = true
			}
		}
	}
	var pick *nodeapi.TargetResult
	for i := range found.Results {
		c := &found.Results[i]
		sni := strings.ToLower(c.SNI)
		if !c.OK || !c.DNSMatch || w.ownNames[sni] || c.Dest == oldDest || slices.ContainsFunc(oldNames, func(s string) bool { return strings.EqualFold(s, sni) }) {
			continue
		}
		if at, ok := t.failed[strings.ToLower(c.Dest)]; ok && w.now.Sub(at) < 24*time.Hour {
			continue
		}
		if pick == nil || used[strings.ToLower(pick.SNI)] && !used[sni] {
			pick = c // results come fastest first
		}
	}
	if pick == nil {
		t.setStuck(x.ID, "no_target")
		return false
	}
	// The scan above took minutes at most; the admin may have edited the inbound meanwhile.
	// Update applies the new target to the row as it is now, not to the round's snapshot.
	_, next, err := t.inbounds.Update(ctx, x.ID, domain.InboundPatch{Dest: &pick.Dest, ServerName: &pick.SNI})
	if err != nil {
		t.log.Error("autotune: new target", "inbound", x.Name, "err", err)
		return false
	}
	old := oldDest
	if len(oldNames) > 0 {
		old = oldNames[0]
	}
	t.changed(ctx, w, n, next, db.AddInboundEventParams{Kind: "sni", OldValue: old, NewValue: pick.SNI, Reason: reason})
	return true
}

// nodeIP is the IPv4 address clients reach the node at: the /24 around it is scanned.
func (t *Tuner) nodeIP(ctx context.Context, w *world, n db.Node) (string, error) {
	host := n.PublicHost
	if n.Address == "" {
		host = w.panelHost
	}
	return scan.ResolveIPv4(ctx, host)
}

// changed records an automatic change of a round and updates the round's view.
func (t *Tuner) changed(ctx context.Context, w *world, n db.Node, next db.Inbound, e db.AddInboundEventParams) {
	ev := t.record(ctx, w.now, n, next, e)
	list := w.inbounds[n.ID]
	for i := range list {
		if list[i].ID == next.ID {
			list[i] = next
		}
	}
	w.events = append(w.events, ev)
}

// record writes down an automatic change (the event the admin's alerts read, the audit
// entry) and pushes it to the node. Clients get it with their next subscription update;
// until then the url-test groups route around.
func (t *Tuner) record(ctx context.Context, now time.Time, n db.Node, next db.Inbound, e db.AddInboundEventParams) db.InboundEvent {
	e.InboundID, e.NodeID, e.CreatedAt = next.ID, n.ID, now.Unix()
	if err := t.st.Q.AddInboundEvent(ctx, e); err != nil {
		t.log.Error("autotune: record event", "err", err)
	}
	_ = audit.Write(ctx, t.st.Q, now, audit.Entry{Action: "auto.inbound_" + e.Kind, TargetType: "inbound", TargetID: next.Name,
		Details: map[string]any{"node": n.ID, "old": e.OldValue, "new": e.NewValue, "reason": e.Reason}})
	t.log.Warn("autotune: inbound changed", "node", n.ID, "inbound", next.Name, "kind", e.Kind, "old", e.OldValue, "new", e.NewValue, "reason", e.Reason)
	t.mu.Lock()
	s := t.stateLocked(next.ID)
	s.CutOff, s.Since, s.Stuck, s.fails = false, time.Time{}, "", 0
	if e.Kind == "sni" {
		s.TargetAt = time.Time{}
	}
	t.mu.Unlock()
	t.changes.SlotsChanged()
	return db.InboundEvent{InboundID: e.InboundID, NodeID: e.NodeID, Kind: e.Kind, Network: e.Network,
		OldValue: e.OldValue, NewValue: e.NewValue, Reason: e.Reason, CreatedAt: e.CreatedAt}
}
