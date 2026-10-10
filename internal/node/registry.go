package node

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"mikan/internal/nodeapi"
)

// Registry holds per-slot policy, traffic counters and live connections.
//
// Lock order: batchMu → mu → slot.mu. Connections are always closed after all locks
// are released: closing unregisters the conn, which takes slot.mu again.
type Registry struct {
	mu      sync.RWMutex
	byKey   map[string]*slot // slot name and uuid → slot
	byName  map[string]*slot
	shared  map[string]*slot  // listeners with one key for everyone, by listener name
	poolOf  map[string]string // listener name → its traffic pool, when it has one
	retired []*slot           // removed slots whose counters are not cut yet
	now     func() time.Time
	release time.Duration

	batchMu sync.Mutex
	epoch   string
	seq     int64
	pending *nodeapi.Counters

	torrentCfg atomic.Pointer[nodeapi.TorrentBlock] // nil: the torrent blocker is off
	torrent    torrents

	ingress atomic.Pointer[ingress] // nil: anyone may connect

	shaper *shaper // speed caps and the fair share (shaper.go)
}

type slot struct {
	name, uuid string
	shared     bool // a listener's one key for everyone: no devices, no counters

	up, down  atomic.Int64 // counted but not yet cut into a batch
	quotaOn   atomic.Bool
	remaining atomic.Int64
	blocked   atomic.Bool // !allowed; read on every connection (exhausted quotas are per bucket)
	// The torrent blocker: the slot is let alone, or kept out until the panel's ban or
	// the node's own short one ends (unix seconds).
	torrentExempt atomic.Bool
	policyBan     atomic.Int64
	localBan      atomic.Int64

	mu          sync.Mutex
	allowed     bool
	exhausted   bool            // the main quota: inbounds outside every pool
	inbounds    map[string]bool // nil = all
	deviceLimit int
	otherIPs    map[string]bool // the slot's devices on other nodes of the panel
	conns       map[*countingConn]struct{}
	ips         map[string]*ipUse
	seen        map[seenKey]time.Time // last admitted connection per device and inbound
	cleaned     time.Time             // when ips was last swept of devices that left
	seenPruned  time.Time             // likewise for seen
	pools       map[string]*bucket    // traffic pools of the slot, made on first use

	group atomic.Pointer[group] // the user's speed group; nil before the first policy
}

type ipUse struct {
	open     int
	lastSeen time.Time
}

type seenKey struct{ ip, inbound string }

// activityKeep bounds how far back Activity reports; the panel looks at the last minutes.
const activityKeep = time.Hour

// seqAfterCrash is how far restore moves the batch counter on: far more batches than the
// counters file can be behind (one is cut at most every couple of seconds, the file is
// written every ten).
const seqAfterCrash = 100_000

func NewRegistry(epoch string, seq int64, release time.Duration, now func() time.Time) *Registry {
	return &Registry{byKey: map[string]*slot{}, byName: map[string]*slot{}, epoch: epoch, seq: seq, release: release, now: now,
		torrent: newTorrents(), shaper: newShaper(now)}
}

func newSlot(name, uuid string) *slot {
	s := &slot{name: name, uuid: uuid, conns: map[*countingConn]struct{}{}, ips: map[string]*ipUse{}, seen: map[seenKey]time.Time{}}
	s.blocked.Store(true) // no policy yet → deny
	return s
}

func (r *Registry) Epoch() string {
	r.batchMu.Lock()
	defer r.batchMu.Unlock()
	return r.epoch
}

// SetSlots replaces the slot set, keeping counters and connections of slots that stay.
func (r *Registry) SetSlots(list []nodeapi.Slot) {
	r.mu.Lock()
	byName := make(map[string]*slot, len(list))
	byKey := make(map[string]*slot, 2*len(list))
	for _, sl := range list {
		s := r.byName[sl.Name]
		if s == nil || s.uuid != sl.UUID {
			s = newSlot(sl.Name, sl.UUID)
		}
		byName[sl.Name] = s
		byKey[sl.Name] = s
		byKey[sl.UUID] = s
	}
	var gone []*countingConn
	for name, s := range r.byName {
		if byName[name] == s {
			continue
		}
		r.retired = append(r.retired, s)
		s.mu.Lock()
		s.allowed = false
		s.blocked.Store(true)
		gone = append(gone, s.connsLocked()...)
		s.mu.Unlock()
	}
	r.byName, r.byKey = byName, byKey
	r.mu.Unlock()
	closeAll(gone)
}

// SetPolicies applies access rules. Slots without a policy are denied.
func (r *Registry) SetPolicies(epoch string, list []nodeapi.Policy) {
	r.batchMu.Lock()
	defer r.batchMu.Unlock()
	given := make(map[string]nodeapi.Policy, len(list))
	for _, p := range list {
		given[p.Slot] = p
	}
	r.mu.RLock()
	slots := make([]*slot, 0, len(r.byName))
	for _, s := range r.byName {
		slots = append(slots, s)
	}
	r.mu.RUnlock()

	var toClose []*countingConn
	groups := map[string]bool{}
	for _, s := range slots {
		p, ok := given[s.name]
		key := s.name
		if ok && p.Group != "" {
			key = p.Group
		}
		g := r.shaper.group(key)
		groups[key] = true
		var own int64
		if ok && p.SpeedMbps > 0 {
			own = int64(p.SpeedMbps) * mbit
		}
		g.own.Store(own)
		s.group.Store(g)
		s.mu.Lock()
		s.allowed = ok && p.Allowed
		s.inbounds = nil
		s.deviceLimit = 0
		s.otherIPs = nil
		s.exhausted = false
		s.quotaOn.Store(false)
		s.torrentExempt.Store(ok && p.TorrentExempt)
		now := r.now().Unix()
		// A ban the panel held and no longer does was lifted there: the node's own short ban
		// for the same catch goes with it.
		if s.policyBan.Load() > now && (!ok || p.BannedUntil <= now) {
			s.localBan.Store(0)
		}
		s.policyBan.Store(0)
		if ok {
			s.policyBan.Store(p.BannedUntil)
			if len(p.Inbounds) > 0 {
				s.inbounds = make(map[string]bool, len(p.Inbounds))
				for _, in := range p.Inbounds {
					s.inbounds[in] = true
				}
			}
			s.deviceLimit = p.DeviceLimit
			if len(p.OtherIPs) > 0 {
				s.otherIPs = make(map[string]bool, len(p.OtherIPs))
				for _, ip := range p.OtherIPs {
					s.otherIPs[ip] = true
				}
			}
			if p.QuotaRemaining >= 0 {
				// Bytes the panel has not seen yet as of BaseSeq still count against the quota.
				after := s.up.Load() + s.down.Load()
				if r.pending != nil && (epoch != r.epoch || r.pending.Seq > p.BaseSeq) {
					t := r.pending.Slots[s.name]
					after += t.Up + t.Down
				}
				rem := p.QuotaRemaining - after
				s.remaining.Store(rem)
				s.quotaOn.Store(true)
				s.exhausted = rem <= 0
			}
		}
		// Pools: a pool the policy does not name has no limit here.
		for _, b := range s.pools {
			b.quotaOn.Store(false)
			b.exhausted.Store(false)
		}
		if ok {
			for _, pq := range p.Pools {
				if pq.Remaining < 0 {
					continue
				}
				b := s.poolLocked(pq.Pool)
				after := b.up.Load() + b.down.Load()
				if r.pending != nil && (epoch != r.epoch || r.pending.Seq > p.BaseSeq) {
					t := r.pending.Pools[s.name][pq.Pool]
					after += t.Up + t.Down
				}
				rem := pq.Remaining - after
				b.remaining.Store(rem)
				b.quotaOn.Store(true)
				b.exhausted.Store(rem <= 0)
			}
		}
		s.blocked.Store(!s.allowed)
		if !s.allowed || s.banned(r.now()) {
			toClose = append(toClose, s.connsLocked()...)
		} else {
			for c := range s.conns {
				switch {
				case s.inbounds != nil && !s.inbounds[c.inName],
					c.bucket == nil && s.exhausted,
					c.bucket != nil && c.bucket.exhausted.Load():
					toClose = append(toClose, c)
				}
			}
		}
		s.mu.Unlock()
	}
	r.shaper.keep(groups)
	r.shaper.share()
	closeAll(toClose)
	r.reshape()
}

// SetShaping turns the node's fair share on or off.
func (r *Registry) SetShaping(s *nodeapi.Shaping) {
	r.shaper.setChannel(s)
	r.reshape()
}

// RunShaper works the fair share out every second until ctx ends.
func (r *Registry) RunShaper(ctx context.Context) { r.shaper.run(ctx) }

// shapedGroup is the group whose ceilings hold the slot's new connections, nil when
// nothing holds them back (no cap, no fair share): they may then be spliced.
func (r *Registry) shapedGroup(s *slot) *group {
	g := s.group.Load()
	if g == nil || s.shared || (g.own.Load() == 0 && !r.shaper.on()) {
		return nil
	}
	return g
}

// reshape closes the connections opened before a cap or the fair share came in: they go
// around the limiter, and the apps open them again at once, held this time.
func (r *Registry) reshape() {
	r.mu.RLock()
	slots := values(r.byName)
	r.mu.RUnlock()
	var toClose []*countingConn
	for _, s := range slots {
		if r.shapedGroup(s) == nil {
			continue
		}
		s.mu.Lock()
		for c := range s.conns {
			if !c.shaped {
				toClose = append(toClose, c)
			}
		}
		s.mu.Unlock()
	}
	closeAll(toClose)
}

func (r *Registry) lookup(key string) *slot {
	if key == "" {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byKey[key]
}

// SetShared names the listeners whose clients carry no user: one key for everyone
// (Shadowsocks-2022, Sudoku, Snell). Their connections go through without a policy or
// limits, and are not counted: no user owns the traffic and the panel has no use for a
// listener's total.
func (r *Registry) SetShared(names []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	next := make(map[string]*slot, len(names))
	for _, n := range names {
		s := r.shared[n]
		if s == nil {
			s = newSlot("~"+n, "")
			s.shared = true
			s.allowed = true
			s.blocked.Store(false)
		}
		next[n] = s
	}
	r.shared = next
}

func (r *Registry) sharedSlot(inName string) *slot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.shared[inName]
}

// admit checks policy and the device limit. For TCP it reserves an open-connection
// slot on the source IP; release it with slot.closeConn.
func (r *Registry) admit(user, inName, ip string, tcp bool) *slot {
	s, _ := r.admitIn(user, inName, ip, tcp)
	return s
}

// admitIn is admit that also says which traffic pool the connection counts to (nil: the
// main quota). An exhausted pool turns away only its own inbounds, and so does the main
// quota.
func (r *Registry) admitIn(user, inName, ip string, tcp bool) (*slot, *bucket) {
	s := r.lookup(user)
	if user == "" {
		s = r.sharedSlot(inName)
	}
	if s == nil || s.blocked.Load() {
		return nil, nil
	}
	if s.shared {
		return s, nil
	}
	now := r.now()
	if s.banned(now) {
		return nil, nil
	}
	pool := r.poolOfListener(inName)
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.allowed || (s.inbounds != nil && !s.inbounds[inName]) {
		return nil, nil
	}
	var b *bucket
	if pool != "" {
		b = s.poolLocked(pool)
		if b.exhausted.Load() {
			return nil, nil
		}
	} else if s.exhausted {
		return nil, nil
	}
	// Devices that left are dropped when a new one asks for a place, and otherwise at most
	// once a second: a busy UDP flow admits every packet and must not walk the slot's
	// devices each time.
	u := s.ips[ip]
	if u == nil || now.Sub(s.cleaned) >= time.Second || u.open == 0 && now.Sub(u.lastSeen) > r.release {
		for k, d := range s.ips {
			if d.open == 0 && now.Sub(d.lastSeen) > r.release {
				delete(s.ips, k)
			}
		}
		s.cleaned = now
		u = s.ips[ip]
	}
	if u == nil {
		// A device already counted on another node is not a new one.
		if s.deviceLimit > 0 && !s.otherIPs[ip] && s.devicesLocked() >= s.deviceLimit {
			return nil, nil
		}
		u = &ipUse{}
		s.ips[ip] = u
	}
	u.lastSeen = now
	if tcp {
		u.open++
	}
	s.seen[seenKey{ip, inName}] = now
	if len(s.seen) > 256 && now.Sub(s.seenPruned) >= time.Minute {
		// Activity prunes too, but a panel that never asks must not grow it forever.
		for k, t := range s.seen {
			if now.Sub(t) > activityKeep {
				delete(s.seen, k)
			}
		}
		s.seenPruned = now
	}
	return s, b
}

// Activity reports, per device, when each inbound last let it in. The panel compares
// inbounds: a device that keeps reaching the node's other inbounds but not one of them is
// cut off from that one on the way (a port blocked by DPI), not idle.
func (r *Registry) Activity() nodeapi.Activity {
	r.mu.RLock()
	slots := values(r.byName)
	r.mu.RUnlock()
	cutoff := r.now().Add(-activityKeep)
	type device struct{ slot, ip string }
	byDevice := map[device]*nodeapi.ClientActivity{}
	for _, s := range slots {
		s.mu.Lock()
		for k, t := range s.seen {
			if t.Before(cutoff) {
				delete(s.seen, k)
				continue
			}
			dk := device{s.name, k.ip}
			c := byDevice[dk]
			if c == nil {
				c = &nodeapi.ClientActivity{Slot: s.name, IP: k.ip, Seen: map[string]int64{}}
				byDevice[dk] = c
			}
			c.Seen[k.inbound] = t.Unix()
		}
		s.mu.Unlock()
	}
	out := nodeapi.Activity{Clients: make([]nodeapi.ClientActivity, 0, len(byDevice))}
	for _, c := range byDevice {
		out.Clients = append(out.Clients, *c)
	}
	sort.Slice(out.Clients, func(i, j int) bool {
		a, b := out.Clients[i], out.Clients[j]
		return a.Slot < b.Slot || a.Slot == b.Slot && a.IP < b.IP
	})
	return out
}

func (s *slot) addConn(c *countingConn) {
	s.mu.Lock()
	s.conns[c] = struct{}{}
	s.mu.Unlock()
}

func (s *slot) closeConn(c *countingConn, now time.Time) {
	s.mu.Lock()
	delete(s.conns, c)
	if u := s.ips[c.ip]; u != nil {
		if u.open > 0 {
			u.open--
		}
		u.lastSeen = now
	}
	s.mu.Unlock()
}

func (s *slot) count(up, down int64) {
	if s.shared {
		return
	}
	if up != 0 {
		s.up.Add(up)
	}
	if down != 0 {
		s.down.Add(down)
	}
	if s.quotaOn.Load() && s.remaining.Add(-(up+down)) <= 0 {
		s.exhaust()
	}
}

func (s *slot) exhaust() {
	s.mu.Lock()
	if s.exhausted {
		s.mu.Unlock()
		return
	}
	s.exhausted = true
	// Only the main quota ran out: connections counted to a pool keep going.
	var conns []*countingConn
	for c := range s.conns {
		if c.bucket == nil {
			conns = append(conns, c)
		}
	}
	s.mu.Unlock()
	// Called from inside a conn's Read/Write path: close asynchronously.
	go closeAll(conns)
}

func (s *slot) connsLocked() []*countingConn {
	out := make([]*countingConn, 0, len(s.conns))
	for c := range s.conns {
		out = append(out, c)
	}
	return out
}

func closeAll(conns []*countingConn) {
	for _, c := range conns {
		_ = c.Close()
	}
}

// Counters returns the outstanding batch, cutting a new one if the previous was acked.
func (r *Registry) Counters() nodeapi.Counters {
	r.batchMu.Lock()
	defer r.batchMu.Unlock()
	r.mu.Lock()
	all := make([]*slot, 0, len(r.byName)+len(r.retired))
	for _, s := range r.byName {
		all = append(all, s)
	}
	if r.pending == nil {
		all = append(all, r.retired...)
		r.retired = nil
	}
	r.mu.Unlock()

	batch := r.pending
	if batch == nil {
		b := &nodeapi.Counters{Epoch: r.epoch, Seq: r.seq + 1, Slots: map[string]nodeapi.Traffic{}}
		for _, s := range all {
			up, down := s.up.Swap(0), s.down.Swap(0)
			if up != 0 || down != 0 {
				t := b.Slots[s.name]
				b.Slots[s.name] = nodeapi.Traffic{Up: t.Up + up, Down: t.Down + down}
			}
			s.mu.Lock()
			for pool, pb := range s.pools {
				up, down := pb.up.Swap(0), pb.down.Swap(0)
				if up == 0 && down == 0 {
					continue
				}
				if b.Pools == nil {
					b.Pools = map[string]map[string]nodeapi.Traffic{}
				}
				if b.Pools[s.name] == nil {
					b.Pools[s.name] = map[string]nodeapi.Traffic{}
				}
				t := b.Pools[s.name][pool]
				b.Pools[s.name][pool] = nodeapi.Traffic{Up: t.Up + up, Down: t.Down + down}
			}
			s.mu.Unlock()
		}
		// A batch without traffic is not cut: it would cost the panel a transaction every
		// couple of seconds to learn nothing. The reply says Idle, so the panel keeps to
		// the live view; an older panel takes it for the last batch again and drops it.
		if len(b.Slots) > 0 || len(b.Pools) > 0 {
			r.seq = b.Seq
			r.pending = b
		} else {
			b.Seq, b.Idle = r.seq, true
		}
		batch = b
	}
	out := *batch
	out.Online = map[string]nodeapi.Online{}
	now := r.now()
	for _, s := range all {
		s.mu.Lock()
		var ips []string
		for ip, u := range s.ips {
			if u.open > 0 || now.Sub(u.lastSeen) <= r.release {
				ips = append(ips, ip)
			}
		}
		n := len(s.conns)
		s.mu.Unlock()
		if n > 0 || len(ips) > 0 {
			sort.Strings(ips)
			out.Online[s.name] = nodeapi.Online{IPs: ips, Conns: n}
		}
	}
	return out
}

// Ack drops the outstanding batch once the panel has stored it.
func (r *Registry) Ack(epoch string, seq int64) bool {
	r.batchMu.Lock()
	defer r.batchMu.Unlock()
	if epoch != r.epoch || r.pending == nil || r.pending.Seq != seq {
		return false
	}
	r.pending = nil
	return true
}

func (r *Registry) ConnCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := 0
	for _, s := range r.byName {
		s.mu.Lock()
		n += len(s.conns)
		s.mu.Unlock()
	}
	return n
}

// counterState is what survives a node restart.
type counterState struct {
	Epoch   string                     `json:"epoch"`
	Seq     int64                      `json:"seq"`
	Pending *nodeapi.Counters          `json:"pending,omitempty"`
	Current map[string]nodeapi.Traffic `json:"current"`
	// CurrentPools: the same for traffic pools, slot → pool.
	CurrentPools map[string]map[string]nodeapi.Traffic `json:"current_pools,omitempty"`
}

func (r *Registry) snapshot() counterState {
	r.batchMu.Lock()
	defer r.batchMu.Unlock()
	st := counterState{Epoch: r.epoch, Seq: r.seq, Pending: r.pending, Current: map[string]nodeapi.Traffic{}}
	r.mu.RLock()
	for _, s := range append(r.retired[:len(r.retired):len(r.retired)], values(r.byName)...) {
		if up, down := s.up.Load(), s.down.Load(); up != 0 || down != 0 {
			t := st.Current[s.name]
			st.Current[s.name] = nodeapi.Traffic{Up: t.Up + up, Down: t.Down + down}
		}
		s.mu.Lock()
		for pool, b := range s.pools {
			if up, down := b.up.Load(), b.down.Load(); up != 0 || down != 0 {
				if st.CurrentPools == nil {
					st.CurrentPools = map[string]map[string]nodeapi.Traffic{}
				}
				if st.CurrentPools[s.name] == nil {
					st.CurrentPools[s.name] = map[string]nodeapi.Traffic{}
				}
				t := st.CurrentPools[s.name][pool]
				st.CurrentPools[s.name][pool] = nodeapi.Traffic{Up: t.Up + up, Down: t.Down + down}
			}
		}
		s.mu.Unlock()
	}
	r.mu.RUnlock()
	return st
}

// restore adds persisted counters to the current slots; unknown slots are kept as retired.
func (r *Registry) restore(st counterState) {
	r.batchMu.Lock()
	defer r.batchMu.Unlock()
	// The file is written every few seconds, so after a crash it is behind the batches the
	// panel has stored since. The panel drops a batch whose seq it has seen already: skip
	// ahead of anything the node could have cut, or the first traffic after a crash would
	// be thrown away as a duplicate. A pending batch keeps its own seq: the panel treats
	// it as the duplicate or the news it is.
	r.epoch, r.seq, r.pending = st.Epoch, st.Seq+seqAfterCrash, st.Pending
	r.mu.Lock()
	defer r.mu.Unlock()
	for name, t := range st.Current {
		s := r.byName[name]
		if s == nil {
			s = newSlot(name, "")
			r.retired = append(r.retired, s)
		}
		s.up.Add(t.Up)
		s.down.Add(t.Down)
	}
	for name, pools := range st.CurrentPools {
		s := r.byName[name]
		if s == nil {
			s = newSlot(name, "")
			r.retired = append(r.retired, s)
		}
		s.mu.Lock()
		for pool, t := range pools {
			b := s.poolLocked(pool)
			b.up.Add(t.Up)
			b.down.Add(t.Down)
		}
		s.mu.Unlock()
	}
}

func values(m map[string]*slot) []*slot {
	out := make([]*slot, 0, len(m))
	for _, s := range m {
		out = append(out, s)
	}
	return out
}

// devicesLocked counts the slot's devices on this node and on the panel's other nodes.
func (s *slot) devicesLocked() int {
	n := len(s.ips)
	for ip := range s.otherIPs {
		if s.ips[ip] == nil {
			n++
		}
	}
	return n
}

// bucket counts one traffic pool of a slot: its own bytes and its own quota.
type bucket struct {
	up, down  atomic.Int64
	quotaOn   atomic.Bool
	remaining atomic.Int64
	exhausted atomic.Bool
}

// poolLocked is the slot's bucket of pool, made on first use; s.mu is held.
func (s *slot) poolLocked(pool string) *bucket {
	if s.pools == nil {
		s.pools = map[string]*bucket{}
	}
	b := s.pools[pool]
	if b == nil {
		b = &bucket{}
		s.pools[pool] = b
	}
	return b
}

// SetPools says which listeners count to which traffic pool; the rest count to the
// main quota.
func (r *Registry) SetPools(poolOf map[string]string) {
	r.mu.Lock()
	r.poolOf = poolOf
	r.mu.Unlock()
}

func (r *Registry) poolOfListener(inName string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.poolOf[inName]
}

// countIn counts bytes to b (nil: the main quota) and cuts the bucket's connections once
// its quota runs out.
func (s *slot) countIn(b *bucket, up, down int64) {
	if s.shared {
		return
	}
	if g := s.group.Load(); g != nil {
		g.moved(up, down)
	}
	if b == nil {
		s.count(up, down)
		return
	}
	if up != 0 {
		b.up.Add(up)
	}
	if down != 0 {
		b.down.Add(down)
	}
	if b.quotaOn.Load() && b.remaining.Add(-(up+down)) <= 0 && !b.exhausted.Swap(true) {
		s.mu.Lock()
		var conns []*countingConn
		for c := range s.conns {
			if c.bucket == b {
				conns = append(conns, c)
			}
		}
		s.mu.Unlock()
		go closeAll(conns)
	}
}
