// Package nodesync keeps the panel's nodes in line with the database: desired state,
// access policies and traffic counters per node (Syncer), and the panel-wide upkeep of
// period resets, the slot pool and devices (Manager).
package nodesync

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/warp"
	"mikan/internal/proto"
)

// Node is the subset of the node API the syncer needs.
type Node interface {
	Apply(ctx context.Context, s nodeapi.DesiredState) (nodeapi.ApplyResult, error)
	SetPolicies(ctx context.Context, epoch string, p []nodeapi.Policy) error
	Counters(ctx context.Context) (nodeapi.Counters, error)
	Ack(ctx context.Context, epoch string, seq int64) error
	Health(ctx context.Context) (nodeapi.Health, error)
}

// TLSSource returns the certificate the node uses for its protocols on TLS (Hysteria2,
// TUIC, AnyTLS, TrustTunnel, VLESS TLS) and the pin links carry for it: the leaf's SHA-256
// when clients cannot trust it, "" when they can.
type TLSSource func() (*nodeapi.TLSFiles, string, error)

// Syncer drives one node.
type Syncer struct {
	id      int64
	m       *Manager
	node    Node
	tls     TLSSource
	local   bool
	address string
	log     *slog.Logger

	policiesDirty chan struct{}
	stateDirty    chan struct{}

	mu          sync.Mutex
	stateKey    string
	policyKey   string
	lastApplied nodeapi.ApplyResult
	// ports are the inbounds' ports in the state of lastApplied, by name; replaced, never
	// changed in place: health views share it.
	ports       map[string]string
	lastPush    time.Time // when the policies last reached the node
	failedKey   string    // the state key the last failed Apply was for
	retry       retry     // the pace of attempts at a node that does not answer
	badInbounds string    // the inbounds left out of the state, as last logged

	// Only the counters loop touches these.
	counterFails int       // consecutive failed pulls
	lastStored   time.Time // when a batch was last stored
	epochPush    time.Time // when a new counter epoch last made the policies go out again
	vetLogged    time.Time
	pos          counterPos // the stored counters position, once read: only this loop writes it
	posKnown     bool

	// Only the torrent loop touches this: the blocker's state last recorded for the node
	// ("0", "1"; "" until read).
	torrentOn string

	health atomic.Pointer[HealthView]
	online atomic.Pointer[map[string]nodeapi.Online]
	// served is the pin of the certificate in the state the node last took ("" for a public
	// one); nil until it took one from this panel process. Links follow it, not the
	// certificate picked now: a node that has not got the new one yet keeps being pinned.
	served atomic.Pointer[string]
}

type HealthView struct {
	OK    bool
	Error string
	// Code says why the node did not answer (nodeapi.Link*), Params the facts its text
	// names; empty while it answers.
	Code   string
	Params map[string]string
	// LastOK is when the node last answered; zero when it has not since the panel started.
	// Since is when the current failure began.
	LastOK time.Time
	Since  time.Time
	// Skew is the node's clock minus the panel's, the round trip taken into account; nil
	// when the node does not say its time (older nodes) or did not answer.
	Skew      *time.Duration
	Health    nodeapi.Health
	Listeners []nodeapi.ListenerStatus
	// Ports are the listeners' ports in the state the node runs, by name (the relay's too,
	// as nodeapi.RelayListener), when that is the state this panel last applied; nil
	// otherwise. A listener's failure is about this port, not about the row's, which may
	// have moved since.
	Ports     map[string]string
	CheckedAt time.Time
}

// HostPorts is what the node reported as listening on its server in this check; nil when
// the node did not answer or does not say.
func (v HealthView) HostPorts() *nodeapi.HostPorts {
	if !v.OK {
		return nil
	}
	return v.Health.Host
}

func newSyncer(m *Manager, id int64, t Target) *Syncer {
	s := &Syncer{id: id, m: m, node: t.Node, tls: t.TLS, local: t.Local, address: t.Address, log: m.log.With("node", id),
		policiesDirty: make(chan struct{}, 1), stateDirty: make(chan struct{}, 1)}
	empty := map[string]nodeapi.Online{}
	s.online.Store(&empty)
	s.health.Store(&HealthView{Error: "not checked yet"})
	return s
}

func (s *Syncer) PoliciesChanged() { signal(s.policiesDirty) }
func (s *Syncer) SlotsChanged()    { signal(s.stateDirty) }

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (s *Syncer) Health() HealthView { return *s.health.Load() }

// Online returns the node's live connection view keyed by slot name.
func (s *Syncer) Online() map[string]nodeapi.Online { return *s.online.Load() }

// run keeps the node in line until ctx ends. State and policies go in one loop, one at a
// time; the counters and the health check have their own, so a node that is slow to
// apply a state does not freeze the traffic accounting or the health the admin sees.
func (s *Syncer) run(ctx context.Context) {
	var wg sync.WaitGroup
	defer wg.Wait()
	wg.Add(3)
	go func() {
		defer wg.Done()
		every(ctx, 2*time.Second, s.pullCounters)
	}()
	go func() {
		defer wg.Done()
		every(ctx, torrentPullEvery, s.pullTorrents)
	}()
	go func() {
		defer wg.Done()
		s.refreshHealth(ctx)
		every(ctx, 5*time.Second, s.refreshHealth)
	}()
	s.applyState(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stateDirty:
			s.applyState(ctx)
		case <-s.policiesDirty:
			// Coalesce bursts (bulk actions) into one push.
			t := time.NewTimer(150 * time.Millisecond)
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
			s.pushPolicies(ctx, true)
		}
	}
}

// every calls fn on a ticker until ctx ends.
func every(ctx context.Context, d time.Duration, fn func(context.Context)) {
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fn(ctx)
		}
	}
}

// desired builds the node's full state from the database.
func (s *Syncer) desired(ctx context.Context) (nodeapi.DesiredState, error) {
	st, _, err := s.desiredPin(ctx)
	return st, err
}

// desiredPin is desired with the pin of the certificate in it.
func (s *Syncer) desiredPin(ctx context.Context) (st nodeapi.DesiredState, pin string, err error) {
	q := s.m.st.Q
	n, err := q.GetNode(ctx, s.id)
	if err != nil {
		return st, "", err
	}
	snap, err := s.m.snapshot(ctx, s.id)
	if err != nil {
		return st, "", err
	}
	inbounds := snap.inbounds
	st.Inbounds = []nodeapi.Inbound{}
	if st.Warp, err = s.warp(ctx, n, inbounds); err != nil {
		return st, "", err
	}
	if st.Relay, st.Exits, err = s.cascade(ctx, n, inbounds); err != nil {
		return st, "", err
	}
	if s.local {
		// The panel runs next to its own node, so its HTTPS port is the self-steal REALITY target.
		if st.SelfStealPort, _, err = settings.Get[int](ctx, s.m.set, settings.KeyPanelPort); err != nil {
			return st, "", err
		}
	}
	var bad []string
	for _, in := range inbounds {
		// A disabled node keeps running but serves nothing.
		if in.NodeID != s.id || in.Enabled == 0 || n.Enabled == 0 {
			continue
		}
		// Saved configs were validated when they were saved, but what holds then may not
		// later: the panel's port moved (REALITY self-steal), or the rules got stricter. The
		// node refuses such a state as a whole, so the inbound stays out of it, and the
		// others, the new users and the policies still reach the node.
		t, err := proto.Parse(in.Config)
		if err == nil {
			err = proto.Validate(t, proto.Options{SelfStealPort: st.SelfStealPort})
		}
		if err != nil {
			bad = append(bad, in.Name+": "+err.Error())
			continue
		}
		ni := nodeapi.Inbound{Name: in.Name, Listen: in.Listen, Port: in.Port, Config: t.JSON()}
		if in.PoolID.Valid {
			ni.Pool = strconv.FormatInt(in.PoolID.Int64, 10)
		}
		st.Inbounds = append(st.Inbounds, ni)
	}
	s.noteBadInbounds(bad)
	for _, sl := range snap.slots {
		st.Slots = append(st.Slots, nodeapi.Slot{Name: sl.Name, UUID: sl.Uuid, Secret: sl.Secret})
	}
	if st.TLS, pin, err = s.tls(); err != nil {
		return st, "", err
	}
	st.Torrent = snap.torrent.Block()
	st.Filters = snap.filters.State()
	if n.FairShare != 0 && n.ChannelMbps.Valid {
		st.Shaping = &nodeapi.Shaping{ChannelMbps: int(n.ChannelMbps.Int64)}
	}
	st.Epoch, st.Policies, _ = s.policiesFrom(snap)
	return st, pin, nil
}

func (s *Syncer) applyState(ctx context.Context) {
	st, pin, err := s.desiredPin(ctx)
	if err != nil {
		s.log.Error("build node state", "err", err)
		return
	}
	key := stateKey(st)
	now := s.m.now()
	s.mu.Lock()
	same := key == s.stateKey
	// A node that refused this very state is not asked again at once: the pause grows
	// while it keeps refusing, and a different state is tried straight away.
	paused := key == s.failedKey && s.retry.waiting(now)
	s.mu.Unlock()
	if same {
		s.sendPolicies(ctx, st.Epoch, st.Policies, s.policiesStale(now))
		return
	}
	if paused {
		return
	}
	rev, err := s.nextRevision(ctx)
	if err != nil {
		s.log.Error("revision", "err", err)
		return
	}
	st.Revision = rev
	res, err := s.node.Apply(ctx, st)
	if err != nil {
		s.mu.Lock()
		s.failedKey = key
		log := s.retry.fail(now, err)
		s.mu.Unlock()
		if log {
			s.log.Warn("apply node state", "err", err)
		}
		return
	}
	if err := s.saveRevision(ctx, rev); err != nil {
		s.log.Error("revision", "err", err)
	}
	for _, l := range res.Listeners {
		if !l.OK {
			s.log.Error("listener failed", "name", l.Name, "err", l.Error)
		}
	}
	ports := make(map[string]string, len(st.Inbounds)+1)
	for _, in := range st.Inbounds {
		ports[in.Name] = in.Port
	}
	if st.Relay != nil {
		ports[nodeapi.RelayListener] = st.Relay.Port
	}
	s.mu.Lock()
	s.stateKey = key
	s.policyKey = policyKey(st.Policies)
	s.lastPush = now
	s.lastApplied = res
	s.ports = ports
	s.failedKey = ""
	recovered := s.retry.ok()
	s.mu.Unlock()
	if recovered {
		s.log.Info("node answers again")
	}
	s.log.Info("node state applied", "revision", rev, "recreated", res.Recreated)
	// The node serves this certificate now: links may follow it (drop the pin for a public
	// one, or pin a self-signed one again).
	if old := s.served.Swap(&pin); old == nil || *old != pin {
		s.m.tlsGen.Add(1)
	}
}

// ServedPin is the pin of the certificate the node serves, as of the last state it took;
// ok is false before it took one from this panel process.
func (s *Syncer) ServedPin() (pin string, ok bool) {
	if p := s.served.Load(); p != nil {
		return *p, true
	}
	return "", false
}

// noteBadInbounds logs the inbounds left out of the node's state when that set changes,
// not at every tick.
func (s *Syncer) noteBadInbounds(bad []string) {
	sort.Strings(bad)
	now := strings.Join(bad, "\n")
	s.mu.Lock()
	changed := now != s.badInbounds
	s.badInbounds = now
	s.mu.Unlock()
	if changed {
		for _, b := range bad {
			s.log.Error("inbound left out of the node's state", "inbound", b)
		}
	}
}

// policiesStale says whether it is time to send the policies although nothing in them
// changed. What the node holds goes out of date by itself, as the user's traffic on the
// other nodes counts against the same quota, and a node sums only its own.
func (s *Syncer) policiesStale(now time.Time) bool {
	every := 5 * time.Minute
	if len(s.m.Syncers()) > 1 {
		every = time.Minute
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return now.Sub(s.lastPush) >= every
}

// pushPolicies sends the policies when they changed (or force says so).
func (s *Syncer) pushPolicies(ctx context.Context, force bool) {
	epoch, ps, _, err := s.policies(ctx)
	if err != nil {
		s.log.Error("build policies", "err", err)
		return
	}
	s.sendPolicies(ctx, epoch, ps, force)
}

func (s *Syncer) sendPolicies(ctx context.Context, epoch string, ps []nodeapi.Policy, force bool) {
	key := policyKey(ps)
	now := s.m.now()
	s.mu.Lock()
	unchanged := key == s.policyKey && !force
	// A node that does not answer would not take these either.
	down := s.retry.unreachable(now)
	s.mu.Unlock()
	if unchanged || down {
		return
	}
	if err := s.node.SetPolicies(ctx, epoch, ps); err != nil {
		s.mu.Lock()
		log := s.retry.fail(now, err)
		s.mu.Unlock()
		if log {
			s.log.Warn("push policies", "err", err)
		}
		return
	}
	s.mu.Lock()
	s.policyKey = key
	s.lastPush = now
	s.mu.Unlock()
}

// policies are the same slots on every node; what differs is the counter position the
// quota refers to, the inbounds that exist here and the devices seen elsewhere. A user
// has the own slot and one per bound device: all of them get the user's rules, and the
// device limit counts the user's devices under all of them.
func (s *Syncer) policies(ctx context.Context) (epoch string, out []nodeapi.Policy, slotUser map[string]int64, err error) {
	snap, err := s.m.snapshot(ctx, s.id)
	if err != nil {
		return "", nil, nil, err
	}
	epoch, out, slotUser = s.policiesFrom(snap)
	return epoch, out, slotUser, nil
}

// policiesFrom builds the policies from one snapshot: the quotas refer to the counters
// position read with them.
func (s *Syncer) policiesFrom(snap *snapshot) (epoch string, out []nodeapi.Policy, slotUser map[string]int64) {
	pos := snap.counters[s.id]
	epoch, seq := pos.epoch, pos.seq
	slotUser = make(map[string]int64, len(snap.owners))
	slotsOf := map[int64][]string{}
	for _, o := range snap.owners {
		slotUser[o.SlotName] = o.UserID
		slotsOf[o.UserID] = append(slotsOf[o.UserID], o.SlotName)
	}
	here := map[int64]string{}
	inPool := map[int64][]int64{} // pool → its inbounds on this node
	for _, in := range snap.inbounds {
		if in.NodeID == s.id {
			here[in.ID] = in.Name
			if in.PoolID.Valid {
				inPool[in.PoolID.Int64] = append(inPool[in.PoolID.Int64], in.ID)
			}
		}
	}
	// The inbounds here each user's tariff leaves out, through the pools they are in.
	shut := map[int64]map[int64]bool{}
	for _, up := range snap.pools {
		if !up.Excluded {
			continue
		}
		for _, id := range inPool[up.PoolID] {
			if shut[up.UserID] == nil {
				shut[up.UserID] = map[int64]bool{}
			}
			shut[up.UserID][id] = true
		}
	}
	others := s.m.otherIPs(s.id, slotUser)
	now := s.m.now()
	grants := snap.grants
	pools := userPoolQuotas(snap.pools, grants)
	for _, u := range snap.users {
		names := slotsOf[u.ID]
		sort.Strings(names)
		for _, name := range names {
			p := userPolicy(u, grants.Main(u.ID), name, seq, now, here, shut[u.ID], others[name])
			p.Pools = pools[u.ID]
			p.TorrentExempt = snap.torrent.IsExempt(u.ID)
			if !p.TorrentExempt {
				// A user added to the exemptions after a catch is let in at once.
				p.BannedUntil = snap.bans[u.ID]
			}
			out = append(out, p)
		}
	}
	return epoch, out, slotUser
}

// userPolicy is the user's rules for one of the user's slots; grants is what is left of
// the user's main grants, added to the quota, and shut the inbounds here the user's tariff
// leaves out (an excluded pool).
func userPolicy(u db.User, grants int64, name string, seq int64, now time.Time, here map[int64]string, shut map[int64]bool, otherIPs []string) nodeapi.Policy {
	// A user whose main traffic ran out still gets in: the node turns away the inbounds
	// outside every pool (QuotaRemaining 0) and keeps the pools that have traffic left.
	state := domain.State(u, grants, now)
	p := nodeapi.Policy{Slot: name, Allowed: domain.CanConnect(state) || state == domain.StateLimited, BaseSeq: seq, OtherIPs: otherIPs,
		QuotaRemaining: domain.TrafficLeft(u.TrafficLimit, u.UsedUp+u.UsedDown, grants)}
	if u.DeviceLimit.Valid {
		p.DeviceLimit = int(u.DeviceLimit.Int64)
	}
	if u.SpeedLimit.Valid {
		p.SpeedMbps = int(u.SpeedLimit.Int64)
	}
	// The user's devices are slots of their own: they share the user's speed and the
	// node's fair share as one.
	p.Group = strconv.FormatInt(u.ID, 10)
	allowed := domain.DecodeInbounds(u.Inbounds)
	if len(allowed) == 0 && len(shut) > 0 {
		// "All" but the excluded: the list is spelled out.
		for id := range here {
			allowed = append(allowed, id)
		}
		slices.Sort(allowed)
	}
	if len(allowed) > 0 {
		for _, id := range allowed {
			if n, ok := here[id]; ok && !shut[id] {
				p.Inbounds = append(p.Inbounds, n)
			}
		}
		// An empty list means "all": a user limited to other nodes' inbounds gets none here.
		if len(p.Inbounds) == 0 {
			p.Allowed = false
		}
	}
	return p
}

// storeEvery is how often a node's traffic goes to PostgreSQL. The counters are pulled
// every two seconds for the live view; storing each batch then was a transaction a second
// with two nodes, and the users' rows rewritten with it.
const storeEvery = 10 * time.Second

// failedPullsBlank is how many pulls in a row may fail before the node's live view is
// forgotten: devices a node last reported are not still there once it has gone quiet, and
// would hold places in the device limit on the other nodes.
const failedPullsBlank = 3

func (s *Syncer) pullCounters(ctx context.Context) {
	c, err := s.node.Counters(ctx)
	if err != nil {
		if s.counterFails++; s.counterFails >= failedPullsBlank {
			none := map[string]nodeapi.Online{}
			s.online.Store(&none)
		}
		return
	}
	s.counterFails = 0
	online := c.Online
	if online == nil {
		online = map[string]nodeapi.Online{}
	}
	s.online.Store(&online)

	epoch, seq, err := s.countersPos(ctx)
	if err != nil {
		s.log.Error("counters position", "err", err)
		return
	}
	if c.Idle && c.Epoch == epoch {
		return // no traffic, nothing cut: only the live view above was of use
	}
	now := s.m.now()
	// A stored batch is acknowledged once storeInterval has passed since it was stored.
	// Until then the node keeps offering it and cuts nothing new, so what comes meanwhile
	// adds up on the node and goes to PostgreSQL in one batch as soon as it is cut: at most
	// about one interval late, nothing lost or counted twice. The node enforces the quotas
	// itself, the batch it holds included.
	ackDue := now.Sub(s.lastStored) >= s.m.storeInterval
	if c.Epoch == epoch && c.Seq <= seq {
		if ackDue || s.lastStored.IsZero() {
			_ = s.node.Ack(ctx, c.Epoch, c.Seq)
		}
		return
	}
	if c.Epoch == epoch && !s.lastStored.IsZero() && !ackDue {
		return // a node that cut a batch anyway (an older one): it keeps it until then
	}
	c = s.vet(c, now)
	names := make([]string, 0, len(c.Slots)+len(c.Pools))
	for slot := range c.Slots {
		names = append(names, slot)
	}
	for slot := range c.Pools {
		if _, ok := c.Slots[slot]; !ok {
			names = append(names, slot)
		}
	}
	// See domain.CountTraffic for why READ COMMITTED is enough: batches of different nodes
	// take turns on the users' rows instead of aborting each other.
	err = s.m.st.TxRC(ctx, func(q *db.Queries) error {
		rows, err := q.SlotOwners(ctx, names)
		if err != nil {
			return err
		}
		owner := make(map[string]int64, len(rows))
		for _, r := range rows {
			owner[r.SlotName] = r.UserID
		}
		// Slots of the same user add up. Traffic past the base quota is taken from the
		// grants on the batch's transaction: a batch delivered again is skipped above,
		// grants included.
		b := domain.TrafficBatch{Main: map[int64]domain.Bytes{}, Pools: map[[2]int64]domain.Bytes{}, Node: s.id}
		for slot, t := range c.Slots {
			if uid, ok := owner[slot]; ok {
				cur := b.Main[uid]
				b.Main[uid] = domain.Bytes{Up: cur.Up + t.Up, Down: cur.Down + t.Down}
			}
		}
		// Pool traffic counts to the pool, not to the main quota; the statistics take all.
		for slot, pools := range c.Pools {
			uid, ok := owner[slot]
			if !ok {
				continue
			}
			for pool, t := range pools {
				id, err := strconv.ParseInt(pool, 10, 64)
				if err != nil {
					continue
				}
				k := [2]int64{uid, id}
				cur := b.Pools[k]
				b.Pools[k] = domain.Bytes{Up: cur.Up + t.Up, Down: cur.Down + t.Down}
			}
		}
		if err := domain.CountTraffic(ctx, q, b, now); err != nil {
			return err
		}
		// Each slot's own total, main and pools: what every bound device used.
		if err := countSlots(ctx, q, c, owner); err != nil {
			return err
		}
		if c.Epoch != epoch {
			if err := q.SetNodeState(ctx, db.SetNodeStateParams{Key: stateKeyOf("counters_epoch", s.id), Value: c.Epoch}); err != nil {
				return err
			}
		}
		return q.SetNodeState(ctx, db.SetNodeStateParams{Key: stateKeyOf("counters_seq", s.id), Value: strconv.FormatInt(c.Seq, 10)})
	})
	if err != nil {
		s.posKnown = false // read it again: the transaction may have committed after all
		s.log.Error("store counters", "err", err)
		return
	}
	s.pos, s.posKnown = counterPos{epoch: c.Epoch, seq: c.Seq}, true
	s.lastStored = now
	s.m.noteBatch(s.id)
	// An idle reply has no batch behind it: there is nothing for the node to drop, and it
	// would answer the acknowledgement with stale_ack. A stored batch otherwise waits for
	// its acknowledgement until storeInterval is over (above); with none, it goes at once.
	if !c.Idle && s.m.storeInterval <= 0 {
		if err := s.node.Ack(ctx, c.Epoch, c.Seq); err != nil {
			s.log.Warn("ack counters", "err", err)
		}
	}
	if c.Epoch != epoch && now.Sub(s.epochPush) >= 30*time.Second {
		// The node started a new counter epoch (fresh volume): re-base its quotas. The
		// policy loop does it, so no two pushes run at once; a node that keeps changing
		// its epoch gets one push in half a minute, not one per batch.
		s.epochPush = now
		signal(s.policiesDirty)
	}
}

// What a node can have carried for one slot between two stored batches: 2.5 GB/s, 20
// Gbit/s, for as long as it has been (and at least ten minutes). Nothing a node reports
// is trusted beyond that: a node is a server somebody else may run.
const maxBytesPerSecond = 2_500_000_000

// vet drops what a node cannot have carried: negative amounts, which would take usage
// from a user and give it to another, and amounts beyond what the link allows. The batch
// itself is still stored and acknowledged, or the node would offer it again for ever.
func (s *Syncer) vet(c nodeapi.Counters, now time.Time) nodeapi.Counters {
	window := 10 * time.Minute
	if !s.lastStored.IsZero() {
		window = max(window, now.Sub(s.lastStored))
	} else {
		window = 7 * 24 * time.Hour // the panel was not running for who knows how long
	}
	limit := int64(window.Seconds()) * maxBytesPerSecond
	sane := func(t nodeapi.Traffic) bool {
		return t.Up >= 0 && t.Down >= 0 && t.Up <= limit && t.Down <= limit
	}
	var dropped []string
	slots := make(map[string]nodeapi.Traffic, len(c.Slots))
	for slot, t := range c.Slots {
		if sane(t) {
			slots[slot] = t
		} else {
			dropped = append(dropped, slot)
		}
	}
	c.Slots = slots
	if len(c.Pools) > 0 {
		pools := make(map[string]map[string]nodeapi.Traffic, len(c.Pools))
		for slot, byPool := range c.Pools {
			for pool, t := range byPool {
				if !sane(t) {
					dropped = append(dropped, slot+"/"+pool)
					continue
				}
				if pools[slot] == nil {
					pools[slot] = map[string]nodeapi.Traffic{}
				}
				pools[slot][pool] = t
			}
		}
		c.Pools = pools
	}
	if len(dropped) > 0 && now.Sub(s.vetLogged) >= time.Minute {
		s.vetLogged = now
		sort.Strings(dropped)
		s.log.Warn("the node reported traffic it cannot have carried: ignored", "slots", len(dropped), "first", dropped[0])
	}
	return c
}

// countersPos is the stored counters position. Only this loop writes it (a removed node's
// rows go with its syncer), so it is read from PostgreSQL once and then kept, instead of
// two reads every two seconds per node.
func (s *Syncer) countersPos(ctx context.Context) (string, int64, error) {
	if s.posKnown {
		return s.pos.epoch, s.pos.seq, nil
	}
	epoch, seq, err := s.readCountersPos(ctx)
	if err != nil {
		return "", 0, err
	}
	s.pos, s.posKnown = counterPos{epoch: epoch, seq: seq}, true
	return epoch, seq, nil
}

func (s *Syncer) readCountersPos(ctx context.Context) (string, int64, error) {
	epoch, err := s.m.st.Q.GetNodeState(ctx, stateKeyOf("counters_epoch", s.id))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", 0, err
	}
	raw, err := s.m.st.Q.GetNodeState(ctx, stateKeyOf("counters_seq", s.id))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", 0, err
	}
	seq, _ := strconv.ParseInt(raw, 10, 64)
	return epoch, seq, nil
}

// nextRevision is the revision the next Apply carries; saveRevision keeps it once the node
// took the state, so attempts that fail do not each cost a write.
func (s *Syncer) nextRevision(ctx context.Context) (int64, error) {
	raw, err := s.m.st.Q.GetNodeState(ctx, stateKeyOf("revision", s.id))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	rev, _ := strconv.ParseInt(raw, 10, 64)
	return rev + 1, nil
}

func (s *Syncer) saveRevision(ctx context.Context, rev int64) error {
	return s.m.st.Q.SetNodeState(ctx, db.SetNodeStateParams{Key: stateKeyOf("revision", s.id), Value: strconv.FormatInt(rev, 10)})
}

func (s *Syncer) refreshHealth(ctx context.Context) {
	sent := s.m.now()
	h, err := s.node.Health(ctx)
	view := &HealthView{CheckedAt: s.m.now()}
	prev := s.health.Load()
	view.LastOK = prev.LastOK
	if err != nil {
		view.Error, view.Code = err.Error(), nodeapi.Classify(err)
		view.Params = nodeapi.LinkParams(view.Code, s.address, err)
		// A failure that goes on keeps its start; a new one, or another kind, starts now.
		view.Since = view.CheckedAt
		if !prev.OK && prev.Code == view.Code && !prev.Since.IsZero() {
			view.Since = prev.Since
		}
		s.health.Store(view)
		return
	}
	view.OK, view.Health, view.Listeners, view.LastOK = true, h, h.Listeners, view.CheckedAt
	if !h.Time.IsZero() {
		// The node read its clock about halfway through the round trip.
		d := h.Time.Sub(sent.Add(view.CheckedAt.Sub(sent) / 2))
		view.Skew = &d
	}
	s.mu.Lock()
	applied := s.lastApplied.Revision
	if applied != 0 && h.Revision == applied {
		view.Ports = s.ports
	}
	s.health.Store(view)
	// The node is back: what was held off for it goes out now.
	back := s.retry.down
	if back {
		s.retry.ok()
	}
	s.mu.Unlock()
	if back {
		signal(s.stateDirty)
		signal(s.policiesDirty)
	}
	// A node that lost its state (new volume, crash before saving) reports an older revision.
	if h.Revision < applied || (applied == 0 && h.Revision == 0) {
		s.mu.Lock()
		s.stateKey = ""
		s.mu.Unlock()
		signal(s.stateDirty)
	}
}

func stateKey(st nodeapi.DesiredState) string {
	raw, _ := json.Marshal(struct {
		I []nodeapi.Inbound
		S []nodeapi.Slot
		T *nodeapi.TLSFiles
		P int
		W *nodeapi.Warp
		R *nodeapi.Relay
		E []nodeapi.Exit
		B *nodeapi.TorrentBlock
		F *nodeapi.Filters
		H *nodeapi.Shaping
	}{st.Inbounds, st.Slots, st.TLS, st.SelfStealPort, st.Warp, st.Relay, st.Exits, st.Torrent, st.Filters, st.Shaping})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// policyKey ignores QuotaRemaining/BaseSeq: they change with every byte and the node
// tracks consumption itself between pushes.
func policyKey(ps []nodeapi.Policy) string {
	h := sha256.New()
	for _, p := range ps {
		raw, _ := json.Marshal([]any{p.Slot, p.Allowed, p.Inbounds, p.DeviceLimit, p.QuotaRemaining < 0, p.OtherIPs, poolKey(p.Pools),
			p.TorrentExempt, p.BannedUntil, p.SpeedMbps, p.Group})
		h.Write(raw)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// warp is the node's WARP outbound, nil when it has none or it is off. Inbounds set to
// WARP on a node without it simply leave directly.
func (s *Syncer) warp(ctx context.Context, n db.Node, inbounds []db.Inbound) (*nodeapi.Warp, error) {
	w, err := s.m.st.Q.GetNodeWarp(ctx, n.ID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && w.Enabled == 0 {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := &nodeapi.Warp{PrivateKey: w.PrivateKey, PeerPublicKey: w.PeerPublicKey, Endpoint: w.Endpoint, IPv4: w.Ipv4, IPv6: w.Ipv6,
		MTU: int(w.Mtu), Inbounds: []string{}}
	if r, err := base64.StdEncoding.DecodeString(w.Reserved); err == nil && len(r) == 3 {
		out.Reserved = r
	}
	var routes []string
	_ = json.Unmarshal([]byte(w.Routes), &routes)
	rs, _ := warp.ParseRoutes(routes)
	out.Domains, out.CIDRs = rs.Domains, rs.CIDRs
	for _, in := range inbounds {
		if in.NodeID == n.ID && in.Enabled != 0 && in.Outbound == "warp" && !in.ExitNodeID.Valid {
			out.Inbounds = append(out.Inbounds, in.Name)
		}
	}
	// Traffic other nodes relay through this one may leave by WARP too.
	if r, err := s.m.st.Q.GetNodeRelay(ctx, n.ID); err == nil && r.Outbound == "warp" && !r.ExitNodeID.Valid {
		out.Inbounds = append(out.Inbounds, nodeapi.RelayListener)
	}
	return out, nil
}

// Warp asks the node how it reaches the internet through WARP; force skips the node's
// minute-old answer.
func (m *Manager) Warp(ctx context.Context, id int64, force bool) (nodeapi.WarpStatus, error) {
	s, ok := m.Syncer(id)
	if !ok {
		return nodeapi.WarpStatus{}, nodeapi.ErrUnavailable
	}
	c, ok := s.node.(interface {
		Warp(ctx context.Context, force bool) (nodeapi.WarpStatus, error)
	})
	if !ok {
		return nodeapi.WarpStatus{}, nodeapi.ErrUnavailable
	}
	return c.Warp(ctx, force)
}

// cascade is the node's part in cascades: its relay listener when other nodes leave
// through it, and the other nodes it sends inbounds (and its own relay) to. Keys and
// relays are made by the API when an exit is chosen; a missing one leaves that exit out,
// and the inbounds behind it fail instead of going direct.
func (s *Syncer) cascade(ctx context.Context, n db.Node, inbounds []db.Inbound) (*nodeapi.Relay, []nodeapi.Exit, error) {
	q := s.m.st.Q
	if n.Enabled == 0 {
		return nil, nil, nil
	}
	var relay *nodeapi.Relay
	own, err := q.GetNodeRelay(ctx, n.ID)
	hasRelay := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, err
	}
	if hasRelay {
		users, err := q.ListRelayUsers(ctx, n.ID)
		if err != nil {
			return nil, nil, err
		}
		if len(users) > 0 {
			t, err := proto.Parse(own.Config)
			if err != nil {
				return nil, nil, err
			}
			relay = &nodeapi.Relay{Port: own.Port, Config: t.JSON()}
			for _, u := range users {
				relay.Users = append(relay.Users, nodeapi.Slot{Name: domain.RelayUserName(u.SrcNodeID), UUID: u.Uuid})
			}
		}
	}
	// Which listeners go to which exit, in the order exits first appear.
	routes := map[int64][]string{}
	var order []int64
	add := func(exit int64, name string) {
		if _, ok := routes[exit]; !ok {
			order = append(order, exit)
		}
		routes[exit] = append(routes[exit], name)
	}
	for _, in := range inbounds {
		if in.NodeID == n.ID && in.Enabled != 0 && in.ExitNodeID.Valid {
			add(in.ExitNodeID.Int64, in.Name)
		}
	}
	if relay != nil && own.ExitNodeID.Valid {
		add(own.ExitNodeID.Int64, nodeapi.RelayListener)
	}
	var exits []nodeapi.Exit
	for _, id := range order {
		e, err := s.exitTo(ctx, n.ID, id)
		if err != nil {
			s.log.Warn("cascade exit", "exit", id, "err", err)
			// Still name the exit, with no way to reach it: the inbounds fail, not leak.
			e = nodeapi.Exit{Name: nodeapi.ExitName(id), Proxy: unreachableExit(id)}
		}
		e.Inbounds = routes[id]
		exits = append(exits, e)
	}
	return relay, exits, nil
}

// exitTo is the outbound from node src to node id's relay.
func (s *Syncer) exitTo(ctx context.Context, src, id int64) (nodeapi.Exit, error) {
	q := s.m.st.Q
	x, err := q.GetNode(ctx, id)
	if err != nil {
		return nodeapi.Exit{}, err
	}
	if x.Enabled == 0 {
		return nodeapi.Exit{}, errors.New("exit node is off")
	}
	r, err := q.GetNodeRelay(ctx, id)
	if err != nil {
		return nodeapi.Exit{}, err
	}
	key, err := q.GetRelayUser(ctx, db.GetRelayUserParams{ExitNodeID: id, SrcNodeID: src})
	if err != nil {
		return nodeapi.Exit{}, err
	}
	host := domain.NodeHost(x)
	if x.Address == "" {
		// The panel's own node: clients reach it at the panel's address.
		ep, err := settings.New(q).Endpoint(ctx)
		if err != nil {
			return nodeapi.Exit{}, err
		}
		host = ep.Host
	}
	port, err := strconv.Atoi(r.Port)
	if err != nil || host == "" {
		return nodeapi.Exit{}, errors.New("exit node has no address")
	}
	t, err := proto.Parse(r.Config)
	if err != nil {
		return nodeapi.Exit{}, err
	}
	c, err := proto.ClientConfig(t, proto.ClientInput{Name: nodeapi.ExitName(id), Host: host, Port: port, Slot: proto.Slot{Name: domain.RelayUserName(src), UUID: key}})
	if err != nil {
		return nodeapi.Exit{}, err
	}
	raw, _ := json.Marshal(c.Mihomo)
	return nodeapi.Exit{Name: nodeapi.ExitName(id), Proxy: raw}, nil
}

// unreachableExit is a VLESS outbound to nowhere (TEST-NET-1, port 9): it keeps an
// exit's rules in place while the exit itself is not usable.
func unreachableExit(id int64) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"name": nodeapi.ExitName(id), "type": "vless", "server": "192.0.2.1", "port": 9,
		"uuid": "00000000-0000-4000-8000-000000000000", "udp": true})
	return raw
}

// Probe asks the node how it reaches the internet through one of its outbounds.
func (m *Manager) Probe(ctx context.Context, id int64, proxy string) (nodeapi.ProbeResult, error) {
	s, ok := m.Syncer(id)
	if !ok {
		return nodeapi.ProbeResult{}, nodeapi.ErrUnavailable
	}
	c, ok := s.node.(interface {
		Probe(ctx context.Context, proxy string) (nodeapi.ProbeResult, error)
	})
	if !ok {
		return nodeapi.ProbeResult{}, nodeapi.ErrUnavailable
	}
	return c.Probe(ctx, proxy)
}

// Tunnel opens a stream to addr through node id: the bot reaches Telegram this way when
// the panel's own server cannot. The node lets through only nodeapi.TunnelHosts.
func (m *Manager) Tunnel(ctx context.Context, id int64, addr string) (net.Conn, error) {
	s, ok := m.Syncer(id)
	if !ok {
		return nil, nodeapi.ErrUnavailable
	}
	c, ok := s.node.(interface {
		Tunnel(ctx context.Context, addr string) (net.Conn, error)
	})
	if !ok {
		return nil, nodeapi.ErrUnavailable
	}
	return c.Tunnel(ctx, addr)
}

// userPoolQuotas are the users' pool quotas with a limit: what is left of each, with the
// pool's grants.
func userPoolQuotas(rows []db.UserPool, grants domain.GrantsLeft) map[int64][]nodeapi.PoolQuota {
	out := map[int64][]nodeapi.PoolQuota{}
	for _, p := range rows {
		if !p.TrafficLimit.Valid {
			continue
		}
		left := domain.TrafficLeft(p.TrafficLimit, p.UsedUp+p.UsedDown, grants.Pool(p.UserID, p.PoolID))
		out[p.UserID] = append(out[p.UserID], nodeapi.PoolQuota{Pool: strconv.FormatInt(p.PoolID, 10), Remaining: left})
	}
	return out
}

// poolKey: which pools have a quota, not how much is left (the node counts that down).
func poolKey(ps []nodeapi.PoolQuota) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Pool)
	}
	return out
}

// countSlots adds a batch to the slots' own totals (slot_traffic): a bound device's slot
// is the device, so the card can say what each one used. Slots of nobody are left out,
// as the users' counters leave them out.
func countSlots(ctx context.Context, q *db.Queries, c nodeapi.Counters, owner map[string]int64) error {
	sum := map[string]nodeapi.Traffic{}
	for slot, t := range c.Slots {
		sum[slot] = nodeapi.Traffic{Up: t.Up, Down: t.Down}
	}
	for slot, pools := range c.Pools {
		cur := sum[slot]
		for _, t := range pools {
			cur.Up, cur.Down = cur.Up+t.Up, cur.Down+t.Down
		}
		sum[slot] = cur
	}
	var p db.AddSlotsTrafficParams
	for slot, t := range sum {
		if _, ok := owner[slot]; !ok || t.Up == 0 && t.Down == 0 {
			continue
		}
		p.Names, p.Up, p.Down = append(p.Names, slot), append(p.Up, t.Up), append(p.Down, t.Down)
	}
	if len(p.Names) == 0 {
		return nil
	}
	return q.AddSlotsTraffic(ctx, p)
}
