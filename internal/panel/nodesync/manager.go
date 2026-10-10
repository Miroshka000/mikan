package nodesync

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"maps"
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
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/torrent"
)

// LocalNode is the id of the panel's own node, reached over the unix socket.
const LocalNode int64 = 1

// Target is how the panel reaches one node.
type Target struct {
	Node Node
	TLS  TLSSource
	// Local is the panel's own node: only it may use the panel as its REALITY target.
	Local bool
	// Address is the node's API host:port, for the words of a failure; "" for the local node.
	Address string
}

// Connect builds the client for a node row; the app knows the socket and certificates.
type Connect func(n db.Node) (Target, error)

// ErrNoNode is what Connect returns for the local node of a panel run without one
// (UI development): the node is skipped quietly.
var ErrNoNode = errors.New("no node")

// Manager runs a Syncer per node and the panel-wide upkeep: period resets, the slot
// pool, devices. Every node gets the same slots and policies, so one subscription works
// on all of them; traffic and devices add up across nodes.
type Manager struct {
	st      *store.Store
	set     *settings.Settings
	pool    *domain.Pool
	connect Connect
	log     *slog.Logger
	now     func() time.Time

	nodesDirty chan struct{}

	mu        sync.Mutex
	running   map[int64]*running
	lastPurge time.Time
	lastPrune time.Time

	generation atomic.Uint64 // moves whenever a node is added, changed or removed
	tlsGen     atomic.Uint64 // moves whenever a node took a certificate with another pin

	// The snapshot the syncers share (snapshot.go): changes moves with every change it
	// may not hold any more, batches counts the traffic batches stored per node.
	changes atomic.Uint64
	snapMu  sync.Mutex
	snap    *snapshot
	batchMu sync.Mutex
	batches map[int64]uint64

	// The torrent blocker's settings for the pullers (torrent.go), read again only after
	// a change: the whole snapshot for one flag every few seconds kept PostgreSQL busy.
	torMu      sync.Mutex
	tor        torrent.Config
	torChanges uint64
	torRead    bool

	// When recordDevices last wrote each user ("u<id>") and device ("d<user>/<ip>"); only
	// the upkeep loop touches it.
	touched map[string]int64

	// storeInterval is storeEvery; tests that pull batch after batch set it to 0.
	storeInterval time.Duration

	// The nodes' last hellos (check.go).
	helloMu sync.Mutex
	hellos  map[int64]HelloView
}

type running struct {
	s      *Syncer
	key    string
	cancel context.CancelFunc
	done   chan struct{}
}

func NewManager(st *store.Store, set *settings.Settings, pool *domain.Pool, connect Connect, log *slog.Logger, now func() time.Time) *Manager {
	return &Manager{st: st, set: set, pool: pool, connect: connect, log: log, now: now,
		nodesDirty: make(chan struct{}, 1), running: map[int64]*running{}, batches: map[int64]uint64{}, storeInterval: storeEvery}
}

func (m *Manager) Run(ctx context.Context) {
	m.reconcile(ctx)
	maintain := time.NewTicker(30 * time.Second)
	defer maintain.Stop()
	for {
		select {
		case <-ctx.Done():
			m.stopAll()
			return
		case <-m.nodesDirty:
			m.reconcile(ctx)
		case <-maintain.C:
			m.maintain(ctx)
		}
	}
}

// PoliciesChanged and SlotsChanged implement domain.Changes for all nodes at once.
func (m *Manager) PoliciesChanged() {
	m.changes.Add(1)
	for _, s := range m.Syncers() {
		s.PoliciesChanged()
	}
}

func (m *Manager) SlotsChanged() {
	m.changes.Add(1)
	for _, s := range m.Syncers() {
		s.SlotsChanged()
	}
}

// NodesChanged restarts syncers after a node was added, removed or re-keyed.
func (m *Manager) NodesChanged() {
	m.generation.Add(1)
	m.changes.Add(1)
	signal(m.nodesDirty)
}

// Generation moves with every NodesChanged, and when a node took a certificate with another
// pin: what is built from the nodes (the subscription's server list) is built again when it
// has moved.
func (m *Manager) Generation() uint64 { return m.generation.Load() + m.tlsGen.Load() }

// ServedPin is the pin of the certificate node id serves ("" for a public one), as of the
// last state it took; ok is false while that is not known (no syncer, or none taken yet).
func (m *Manager) ServedPin(id int64) (pin string, ok bool) {
	s, found := m.Syncer(id)
	if !found {
		return "", false
	}
	return s.ServedPin()
}

// Syncers returns the running syncers ordered by node id.
func (m *Manager) Syncers() []*Syncer {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Syncer, 0, len(m.running))
	for _, r := range m.running {
		out = append(out, r.s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

// Syncer returns the syncer of one node.
func (m *Manager) Syncer(id int64) (*Syncer, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.running[id]
	if !ok {
		return nil, false
	}
	return r.s, true
}

// Online merges the live view of all nodes: a device on two nodes counts once.
func (m *Manager) Online() map[string]nodeapi.Online {
	out := map[string]nodeapi.Online{}
	for _, s := range m.Syncers() {
		for slot, on := range s.Online() {
			cur := out[slot]
			cur.Conns += on.Conns
			for _, ip := range on.IPs {
				if !slices.Contains(cur.IPs, ip) {
					cur.IPs = append(cur.IPs, ip)
				}
			}
			out[slot] = cur
		}
	}
	return out
}

// otherIPs returns, per slot of node id, the user's devices online anywhere else: on the
// other nodes, and under the user's other slots (bound devices) on any node. owner maps
// every slot to its user.
func (m *Manager) otherIPs(id int64, owner map[string]int64) map[string][]string {
	type place struct {
		node int64
		slot string
	}
	byUser := map[int64]map[string][]place{} // user → ip → where it is online
	for _, s := range m.Syncers() {
		for slot, on := range s.Online() {
			uid, ok := owner[slot]
			if !ok {
				continue
			}
			if byUser[uid] == nil {
				byUser[uid] = map[string][]place{}
			}
			for _, ip := range on.IPs {
				byUser[uid][ip] = append(byUser[uid][ip], place{s.id, slot})
			}
		}
	}
	out := map[string][]string{}
	for slot, uid := range owner {
		for ip, where := range byUser[uid] {
			if len(where) == 1 && where[0] == (place{id, slot}) {
				continue // only this very slot here: the node counts it itself
			}
			out[slot] = append(out[slot], ip)
		}
		slices.Sort(out[slot])
	}
	return out
}

// reconcile starts a syncer for every node and restarts it when its address or
// certificate changes.
func (m *Manager) reconcile(ctx context.Context) {
	nodes, err := m.st.Q.ListNodes(ctx)
	if err != nil {
		m.log.Error("list nodes", "err", err)
		return
	}
	want := map[int64]db.Node{}
	for _, n := range nodes {
		want[n.ID] = n
	}
	m.mu.Lock()
	var stop []*running
	for id, r := range m.running {
		if n, ok := want[id]; !ok || nodeKey(n) != r.key {
			stop = append(stop, r)
			delete(m.running, id)
		}
	}
	m.mu.Unlock()
	for _, r := range stop {
		r.cancel()
		<-r.done
	}
	for _, n := range nodes {
		m.mu.Lock()
		_, ok := m.running[n.ID]
		m.mu.Unlock()
		if ok {
			continue
		}
		t, err := m.connect(n)
		if err != nil {
			if !errors.Is(err, ErrNoNode) {
				m.log.Error("connect node", "node", n.ID, "err", err)
			}
			continue
		}
		s := newSyncer(m, n.ID, t)
		sctx, cancel := context.WithCancel(ctx)
		r := &running{s: s, key: nodeKey(n), cancel: cancel, done: make(chan struct{})}
		m.mu.Lock()
		m.running[n.ID] = r
		m.mu.Unlock()
		go func() {
			defer close(r.done)
			s.run(sctx)
		}()
	}
}

// nodeKey changes when the syncer must restart: a new address or key, or a new name
// in the node's Hysteria2/TUIC certificate.
func nodeKey(n db.Node) string {
	return strings.Join([]string{n.Address, n.CertSha256, n.PublicHost, n.Domain}, "|")
}

func (m *Manager) stopAll() {
	m.mu.Lock()
	all := make([]*running, 0, len(m.running))
	for id, r := range m.running {
		all = append(all, r)
		delete(m.running, id)
	}
	m.mu.Unlock()
	for _, r := range all {
		r.cancel()
		<-r.done
	}
}

// Health is the last health check of a node.
func (m *Manager) Health(id int64) (HealthView, bool) {
	s, ok := m.Syncer(id)
	if !ok {
		return HealthView{}, false
	}
	return s.Health(), true
}

// HostPorts is what the node last said listens on its server; nil when it has not (domain.HostLookup).
func (m *Manager) HostPorts(id int64) *nodeapi.HostPorts {
	hv, ok := m.Health(id)
	if !ok {
		return nil
	}
	return hv.HostPorts()
}

// Validate runs mihomo's parser on an inbound on the node that will run it.
func (m *Manager) Validate(ctx context.Context, id int64, req nodeapi.ValidateRequest) error {
	v, err := clientOf[interface {
		Validate(context.Context, nodeapi.ValidateRequest) error
	}](m, id)
	if err != nil {
		return err
	}
	return v.Validate(ctx, req)
}

// RequestUpdate asks a node to update to a release (nodeupdate has the rules).
func (m *Manager) RequestUpdate(ctx context.Context, id int64, version string) error {
	c, err := clientOf[interface {
		RequestUpdate(context.Context, string) error
	}](m, id)
	if err != nil {
		return err
	}
	return c.RequestUpdate(ctx, version)
}

// Activity reports which inbounds of a node each device reached lately.
func (m *Manager) Activity(ctx context.Context, id int64) (nodeapi.Activity, error) {
	c, err := clientOf[interface {
		Activity(context.Context) (nodeapi.Activity, error)
	}](m, id)
	if err != nil {
		return nodeapi.Activity{}, err
	}
	return c.Activity(ctx)
}

// CheckTarget tests a REALITY target from the node that dials it.
func (m *Manager) CheckTarget(ctx context.Context, id int64, req nodeapi.TargetCheckRequest) (nodeapi.TargetResult, error) {
	c, err := clientOf[interface {
		CheckTarget(context.Context, nodeapi.TargetCheckRequest) (nodeapi.TargetResult, error)
	}](m, id)
	if err != nil {
		return nodeapi.TargetResult{}, err
	}
	return c.CheckTarget(ctx, req)
}

// ScanTargets looks for REALITY targets next to a node, from the node itself.
func (m *Manager) ScanTargets(ctx context.Context, id int64, req nodeapi.TargetScanRequest) (nodeapi.TargetScan, error) {
	c, err := clientOf[interface {
		ScanTargets(context.Context, nodeapi.TargetScanRequest) (nodeapi.TargetScan, error)
	}](m, id)
	if err != nil {
		return nodeapi.TargetScan{}, err
	}
	return c.ScanTargets(ctx, req)
}

// SpeedTest runs a node's speed test of its own way to the internet.
func (m *Manager) SpeedTest(ctx context.Context, id int64) (nodeapi.SpeedTest, error) {
	c, err := clientOf[interface {
		SpeedTest(context.Context) (nodeapi.SpeedTest, error)
	}](m, id)
	if err != nil {
		return nodeapi.SpeedTest{}, err
	}
	return c.SpeedTest(ctx)
}

// clientOf returns a node's client as T: methods beyond Node are optional, and tests
// stand in for nodes with fakes that have only some of them.
func clientOf[T any](m *Manager, id int64) (T, error) {
	var zero T
	s, ok := m.Syncer(id)
	if !ok {
		return zero, nodeapi.ErrUnavailable
	}
	c, ok := s.node.(T)
	if !ok {
		return zero, nodeapi.ErrUnavailable
	}
	return c, nil
}

// Retire tells a node being removed from the panel to drop its listeners and users.
// Call it after the node's row is deleted: its syncer stops first, so it cannot push the
// old state back, and reconcile does not start it again.
func (m *Manager) Retire(ctx context.Context, id int64) error {
	m.forgetHello(id)
	m.mu.Lock()
	r, ok := m.running[id]
	delete(m.running, id)
	m.mu.Unlock()
	if !ok {
		return nodeapi.ErrUnavailable
	}
	r.cancel()
	<-r.done
	_, err := r.s.node.Apply(ctx, nodeapi.DesiredState{Inbounds: []nodeapi.Inbound{}, Slots: []nodeapi.Slot{}})
	return err
}

func (m *Manager) maintain(ctx context.Context) {
	now := m.now()
	if err := m.resetPeriods(ctx, now); err != nil {
		m.log.Error("period resets", "err", err)
	}
	m.maintainPool(ctx, now)
	m.prune(ctx, now)
	if err := m.recordDevices(ctx, now); err != nil {
		m.log.Error("record devices", "err", err)
	}
	m.reconcile(ctx)
	// One fresh snapshot for the round: it picks up what changed outside the API.
	m.changes.Add(1)
	for _, s := range m.Syncers() {
		// Reconcile: picks up changes made outside the API (server CLI, restore) and pushes
		// policies, whose key changes by time alone when a user crosses expires_at, and which
		// are sent again when they are old: a user's quota and devices span nodes and bound
		// devices (a slot each), while each node counts per slot.
		s.SlotsChanged()
	}
}

// How long traffic by the hour and the devices that went quiet are kept, and how often
// the old rows are deleted: the tables are big and the delete scans them.
const (
	hourlyKeep = 62 * 24 * time.Hour
	deviceKeep = 30 * 24 * time.Hour
	pruneEvery = time.Hour
)

func (m *Manager) prune(ctx context.Context, now time.Time) {
	if !m.lastPrune.IsZero() && now.Sub(m.lastPrune) < pruneEvery {
		return
	}
	if err := m.st.Q.PruneTrafficHourly(ctx, now.Add(-hourlyKeep).Unix()/3600); err != nil {
		m.log.Error("prune traffic", "err", err)
		return
	}
	// The nodes' traffic is a view of the users': failing it must not keep the rest.
	if err := m.st.Q.PruneNodeTrafficHourly(ctx, now.Add(-hourlyKeep).Unix()/3600); err != nil {
		m.log.Error("prune node traffic", "err", err)
	}
	if err := m.st.Q.PruneDevices(ctx, now.Add(-deviceKeep).Unix()); err != nil {
		m.log.Error("prune devices", "err", err)
		return
	}
	if err := m.st.Q.PruneTorrentHits(ctx, now.Add(-torrentKeep).Unix()); err != nil {
		m.log.Error("prune torrent hits", "err", err)
		return
	}
	m.lastPrune = now
}

func (m *Manager) resetPeriods(ctx context.Context, now time.Time) error {
	users, err := m.st.Q.ListUsers(ctx)
	if err != nil {
		return err
	}
	changed := false
	for _, u := range users {
		start := u.PeriodStart
		switch u.ResetStrategy {
		case "month_start":
			// Monthly on the billing day, or the 1st without one.
			monthStart := domain.MonthPeriodStart(now, u.BillingDay).Unix()
			if start >= monthStart {
				continue
			}
			start = monthStart
		case "period":
			length := max(u.PeriodDays, 1) * 86400
			if now.Unix() < start+length {
				continue
			}
			start += (now.Unix() - start) / length * length
		default:
			continue
		}
		// The row was read above, outside the transaction: the reset happens only while the
		// period is still older than start, so one a payment began meanwhile stays.
		var reset bool
		err := m.st.TxRC(ctx, func(q *db.Queries) (err error) {
			reset, err = domain.StartPeriodIfOlder(ctx, q, u.ID, start, now)
			return err
		})
		if err != nil {
			return err
		}
		changed = changed || reset
	}
	if changed {
		m.PoliciesChanged()
	}
	return nil
}

func (m *Manager) maintainPool(ctx context.Context, now time.Time) {
	stats, err := m.pool.Stats(ctx)
	if err != nil {
		m.log.Error("pool stats", "err", err)
		return
	}
	quietHour, _, err := settings.Get[int](ctx, m.set, settings.KeyQuietHour)
	if err != nil {
		m.log.Error("quiet hour", "err", err)
	}
	inQuietHour := now.UTC().Hour() == quietHour && now.Sub(m.lastPurge) > 20*time.Hour
	var refill, purge bool
	switch {
	case stats.Free < domain.CriticalFree:
		refill = true
	case inQuietHour && stats.Free < domain.LowWatermark:
		refill, purge = true, true
	case inQuietHour && stats.Burned > 0:
		purge = true
	}
	if !refill && !purge {
		return
	}
	if purge {
		if err := m.pool.PurgeBurned(ctx); err != nil {
			m.log.Error("purge slots", "err", err)
			return
		}
		m.lastPurge = now
	}
	if refill {
		if err := m.pool.Refill(ctx, domain.RefillBatch); err != nil {
			m.log.Error("refill slots", "err", err)
			return
		}
	}
	m.log.Info("slot pool maintained", "free", stats.Free, "refill", refill, "purge", purge)
	m.SlotsChanged()
}

// touchOnline is how often an online user's online_at and a device's last_seen are written.
const touchOnline = 2 * time.Minute

func (m *Manager) recordDevices(ctx context.Context, now time.Time) (err error) {
	online := m.Online()
	if len(online) == 0 {
		return nil
	}
	names := make([]string, 0, len(online))
	for slot := range online {
		names = append(names, slot)
	}
	rows, err := m.st.Q.SlotOwners(ctx, names)
	if err != nil {
		return err
	}
	// A user's slots (the own one, bound devices) may report the same address.
	type device struct {
		user int64
		ip   string
	}
	seen := map[int64]bool{}
	var users []int64
	var devices []device
	for _, r := range rows {
		if !seen[r.UserID] {
			seen[r.UserID] = true
			users = append(users, r.UserID)
		}
		for _, ip := range online[r.SlotName].IPs {
			devices = append(devices, device{r.UserID, ip})
		}
	}
	// "Seen" needs minutes, not the upkeep's 30 s: a user or a device written lately is left
	// alone, and with nobody to write there is no transaction and no row lock at all.
	at := now.Unix()
	if m.touched == nil {
		m.touched = map[string]int64{}
	}
	fresh := func(key string) bool {
		return at-m.touched[key] < int64(touchOnline/time.Second) && at >= m.touched[key]
	}
	users = slices.DeleteFunc(users, func(u int64) bool { return fresh("u" + strconv.FormatInt(u, 10)) })
	devices = slices.DeleteFunc(devices, func(d device) bool { return fresh("d" + strconv.FormatInt(d.user, 10) + "/" + d.ip) })
	if len(users) == 0 && len(devices) == 0 {
		return nil
	}
	// A device is upserted under its user's lock: its user is written then too.
	for _, d := range devices {
		if !slices.Contains(users, d.user) {
			users = append(users, d.user)
		}
	}
	slices.Sort(users)
	slices.SortFunc(devices, func(a, b device) int { return cmp.Or(cmp.Compare(a.user, b.user), strings.Compare(a.ip, b.ip)) })
	devices = slices.Compact(devices)
	dp := db.UpsertDevicesParams{Now: at}
	for _, d := range devices {
		dp.UserIds, dp.Ips = append(dp.UserIds, d.user), append(dp.Ips, d.ip)
	}
	defer func() {
		if err == nil {
			for _, u := range users {
				m.touched["u"+strconv.FormatInt(u, 10)] = at
			}
			for _, d := range devices {
				m.touched["d"+strconv.FormatInt(d.user, 10)+"/"+d.ip] = at
			}
			// What has not been written for a while is gone from the live view: forgotten.
			maps.DeleteFunc(m.touched, func(_ string, t int64) bool { return at-t > int64(time.Hour/time.Second) })
		}
	}()
	// Blind writes: READ COMMITTED. The users are locked in id order like the traffic
	// batches lock them; a user deleted meanwhile is skipped, not an error for the rest.
	return m.st.TxRC(ctx, func(q *db.Queries) error {
		locked, err := q.LockUsers(ctx, users)
		if err != nil || len(locked) == 0 {
			return err
		}
		if err := q.SetUsersOnline(ctx, db.SetUsersOnlineParams{OnlineAt: sql.NullInt64{Int64: now.Unix(), Valid: true}, Ids: locked}); err != nil {
			return err
		}
		if len(dp.UserIds) == 0 {
			return nil
		}
		return q.UpsertDevices(ctx, dp)
	})
}

// stateKeyOf names a node_state entry of one node.
func stateKeyOf(name string, id int64) string { return name + "/" + strconv.FormatInt(id, 10) }
