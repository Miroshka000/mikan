package nodesync

import (
	"context"
	"crypto/x509"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/nodetls"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/store/storetest"
)

type fakeNode struct {
	batch    nodeapi.Counters
	acked    []int64
	applied  []nodeapi.DesiredState
	policies [][]nodeapi.Policy
	health   nodeapi.Health
}

func (f *fakeNode) Apply(_ context.Context, s nodeapi.DesiredState) (nodeapi.ApplyResult, error) {
	f.applied = append(f.applied, s)
	return nodeapi.ApplyResult{Revision: s.Revision}, nil
}
func (f *fakeNode) SetPolicies(_ context.Context, _ string, p []nodeapi.Policy) error {
	f.policies = append(f.policies, p)
	return nil
}
func (f *fakeNode) Counters(context.Context) (nodeapi.Counters, error) { return f.batch, nil }
func (f *fakeNode) Ack(_ context.Context, _ string, seq int64) error {
	f.acked = append(f.acked, seq)
	return nil
}
func (f *fakeNode) Health(context.Context) (nodeapi.Health, error) { return f.health, nil }

func fakeTLS() (*nodeapi.TLSFiles, string, error) {
	return &nodeapi.TLSFiles{CertPEM: "c", KeyPEM: "k"}, "pin", nil
}

func setup(t *testing.T) (*Syncer, *fakeNode, *store.Store, *domain.Users, *time.Time) {
	t.Helper()
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := domain.Seed(ctx, st, now); err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return now }
	pool := domain.NewPool(st, clock)
	m := NewManager(st, settings.New(st.Q), pool, func(db.Node) (Target, error) { return Target{}, ErrNoNode },
		slog.New(slog.NewTextHandler(io.Discard, nil)), clock)
	node := &fakeNode{}
	s := m.attach(t, LocalNode, Target{Node: node, TLS: fakeTLS, Local: true})
	return s, node, st, domain.NewUsers(st, pool, m, clock), &now
}

// attach registers a syncer without its loop, so a test drives it step by step.
func (m *Manager) attach(t *testing.T, id int64, target Target) *Syncer {
	t.Helper()
	n, err := m.st.Q.GetNode(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	s := newSyncer(m, id, target)
	done := make(chan struct{})
	close(done)
	m.mu.Lock()
	m.running[id] = &running{s: s, key: nodeKey(n), cancel: func() {}, done: done}
	m.mu.Unlock()
	return s
}

func TestCountersAppliedOnce(t *testing.T) {
	s, node, st, users, _ := setup(t)
	s.m.storeInterval = 0 // every batch acknowledged at once
	ctx := context.Background()
	tariffs, _ := st.Q.ListTariffs(ctx)
	u, err := users.Create(ctx, domain.CreateInput{Name: "a", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	slot, _ := st.Q.GetSlot(ctx, u.SlotID.Int64)
	node.batch = nodeapi.Counters{Epoch: "e1", Seq: 1, Slots: map[string]nodeapi.Traffic{slot.Name: {Up: 100, Down: 900}, "s999999": {Up: 5}}}
	s.pullCounters(ctx)
	s.pullCounters(ctx) // ack lost → the node re-delivers the same batch
	got, _ := st.Q.GetUser(ctx, u.ID)
	if got.UsedUp != 100 || got.UsedDown != 900 || got.TotalDown != 900 {
		t.Fatalf("traffic applied twice or lost: up %d down %d", got.UsedUp, got.UsedDown)
	}
	if len(node.acked) != 2 || node.acked[1] != 1 {
		t.Fatalf("acks = %v, want the duplicate acked too", node.acked)
	}
	node.batch = nodeapi.Counters{Epoch: "e2", Seq: 1, Slots: map[string]nodeapi.Traffic{slot.Name: {Down: 50}}}
	s.pullCounters(ctx)
	got, _ = st.Q.GetUser(ctx, u.ID)
	if got.UsedDown != 950 {
		t.Fatalf("new epoch with seq 1 must be applied, down = %d", got.UsedDown)
	}
	select {
	case <-s.policiesDirty: // the policy loop pushes them, with the quotas re-based
	default:
		t.Fatal("epoch change must re-push policies")
	}
}

// A batch is stored as soon as the node cuts it, and acknowledged storeEvery later: until
// then the node keeps offering it and cuts nothing new, so the next batch holds what came
// in that time and the database gets one transaction per interval. Nothing is lost or
// counted twice; a new epoch is stored at once.
func TestCountersStoredEveryInterval(t *testing.T) {
	s, node, st, users, now := setup(t)
	ctx := context.Background()
	tariffs, _ := st.Q.ListTariffs(ctx)
	u, err := users.Create(ctx, domain.CreateInput{Name: "a", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	slot, _ := st.Q.GetSlot(ctx, u.SlotID.Int64)
	used := func() int64 {
		t.Helper()
		got, err := st.Q.GetUser(ctx, u.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got.UsedDown
	}
	batch := func(epoch string, seq, down int64) {
		node.batch = nodeapi.Counters{Epoch: epoch, Seq: seq, Slots: map[string]nodeapi.Traffic{slot.Name: {Down: down}}}
	}

	batch("e1", 1, 100)
	s.pullCounters(ctx) // stored at once, acknowledged later
	if used() != 100 || len(node.acked) != 0 {
		t.Fatalf("first batch: used %d, acks %v", used(), node.acked)
	}
	*now = now.Add(2 * time.Second)
	s.pullCounters(ctx) // offered again: already stored, not acknowledged yet
	if used() != 100 || len(node.acked) != 0 {
		t.Fatalf("within %s: used %d, acks %v", storeEvery, used(), node.acked)
	}
	*now = now.Add(storeEvery)
	s.pullCounters(ctx) // the interval is over: acknowledged, the node may cut the next
	if used() != 100 || !slices.Equal(node.acked, []int64{1}) {
		t.Fatalf("after %s: used %d, acks %v", storeEvery, used(), node.acked)
	}
	batch("e1", 2, 50)
	*now = now.Add(2 * time.Second)
	s.pullCounters(ctx) // what came meanwhile, in one batch, stored at once
	if used() != 150 || !slices.Equal(node.acked, []int64{1}) {
		t.Fatalf("next batch: used %d, acks %v", used(), node.acked)
	}
	batch("e2", 1, 7)
	*now = now.Add(time.Second)
	s.pullCounters(ctx) // a new epoch does not wait
	if used() != 157 {
		t.Fatalf("new epoch: used %d, acks %v", used(), node.acked)
	}
	// The position the loop keeps is what PostgreSQL holds.
	epoch, seq, err := s.readCountersPos(ctx)
	if err != nil || epoch != "e2" || seq != 1 || s.pos != (counterPos{epoch: "e2", seq: 1}) {
		t.Fatalf("stored position %q/%d (%v), kept %+v", epoch, seq, err, s.pos)
	}
}

func TestPolicies(t *testing.T) {
	s, _, st, users, now := setup(t)
	ctx := context.Background()
	tariffs, _ := st.Q.ListTariffs(ctx)
	quota, _ := users.Create(ctx, domain.CreateInput{Name: "quota", TariffID: tariffs[1].ID})
	unlimited, _ := users.Create(ctx, domain.CreateInput{Name: "unl", TariffID: tariffs[2].ID})
	off, _ := users.Create(ctx, domain.CreateInput{Name: "off", TariffID: tariffs[2].ID})
	yes := true
	if _, err := users.Update(ctx, off.ID, domain.Patch{Disabled: &yes}); err != nil {
		t.Fatal(err)
	}
	if err := st.Q.AddUserTraffic(ctx, dbTraffic(quota.ID, 1<<30)); err != nil {
		t.Fatal(err)
	}
	_, ps, owners, err := s.policies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	by := map[int64]nodeapi.Policy{}
	for _, p := range ps {
		by[owners[p.Slot]] = p
	}
	if p := by[quota.ID]; !p.Allowed || p.QuotaRemaining != 149<<30 || p.DeviceLimit != 3 {
		t.Fatalf("quota user policy %+v", p)
	}
	if p := by[unlimited.ID]; !p.Allowed || p.QuotaRemaining != -1 {
		t.Fatalf("unlimited policy %+v", p)
	}
	if by[off.ID].Allowed {
		t.Fatal("disabled user allowed")
	}
	*now = now.Add(31 * 24 * time.Hour)
	_, ps, owners, _ = s.policies(ctx)
	for _, p := range ps {
		if p.Allowed {
			t.Fatalf("expired user %d still allowed", owners[p.Slot])
		}
	}
}

// The server CLI writes inbounds straight to the database; the running panel must push
// them to the node without a restart.
func TestMaintainAppliesChangesMadeOutsideTheAPI(t *testing.T) {
	s, node, st, _, _ := setup(t)
	ctx := context.Background()
	reconcile := func() {
		t.Helper()
		s.m.maintain(ctx)
		select {
		case <-s.stateDirty:
			s.applyState(ctx)
		default:
			t.Fatal("maintain must ask the node to reconcile")
		}
	}
	s.applyState(ctx)
	reconcile()
	base := len(node.applied)
	reconcile()
	if len(node.applied) != base {
		t.Fatalf("an unchanged state was applied again: %d → %d", base, len(node.applied))
	}
	if _, err := domain.NewInbounds(st, nil, time.Now).Create(ctx, domain.NewInbound{NodeID: LocalNode, Preset: "trojan_reality"}); err != nil {
		t.Fatal(err)
	}
	reconcile()
	if len(node.applied) != base+1 {
		t.Fatalf("the new inbound was not applied: %d applies", len(node.applied))
	}
	var ports []string
	for _, in := range node.applied[len(node.applied)-1].Inbounds {
		ports = append(ports, in.Port)
	}
	if !slices.Contains(ports, "2087") {
		t.Fatalf("applied inbounds: %v", ports)
	}
}

// addRemote registers a second node the way the panel does and attaches a fake for it.
func addRemote(t *testing.T, s *Syncer, st *store.Store) (*Syncer, *fakeNode) {
	t.Helper()
	panel, err := nodetls.Generate("mikan-panel", x509.ExtKeyUsageClientAuth, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	n, _, err := domain.AddNode(context.Background(), st, panel, domain.NodeInput{Name: "🇺🇸 США", Host: "198.51.100.20", APIPort: 40000}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeNode{}
	return s.m.attach(t, n.ID, Target{Node: fake, TLS: fakeTLS}), fake
}

func TestEachNodeGetsItsOwnInbounds(t *testing.T) {
	local, _, st, _, _ := setup(t)
	ctx := context.Background()
	remote, _ := addRemote(t, local, st)
	if err := settings.Set(ctx, settings.New(st.Q), settings.KeyPanelPort, 21355); err != nil {
		t.Fatal(err)
	}
	a, err := local.desired(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, err := remote.desired(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Inbounds) != 4 || len(b.Inbounds) != 4 {
		t.Fatalf("each node runs its own default inbounds: %d and %d", len(a.Inbounds), len(b.Inbounds))
	}
	// Only the panel's own node may use the panel as its REALITY target.
	if a.SelfStealPort != 21355 || b.SelfStealPort != 0 {
		t.Fatalf("self-steal ports: %d and %d", a.SelfStealPort, b.SelfStealPort)
	}
	if len(a.Slots) == 0 || len(a.Slots) != len(b.Slots) {
		t.Fatalf("all nodes get the same slots: %d and %d", len(a.Slots), len(b.Slots))
	}

	// A disabled node keeps its syncer but serves nothing.
	n, _ := st.Q.GetNode(ctx, remote.id)
	if _, err := st.Q.UpdateNode(ctx, db.UpdateNodeParams{Name: n.Name, Address: n.Address, PublicHost: n.PublicHost, Domain: n.Domain, PublicName: n.PublicName, Enabled: 0, ID: n.ID}); err != nil {
		t.Fatal(err)
	}
	if b, _ = remote.desired(ctx); len(b.Inbounds) != 0 {
		t.Fatalf("a disabled node must stop its listeners: %d", len(b.Inbounds))
	}
}

func TestPoliciesAcrossNodes(t *testing.T) {
	local, _, st, users, _ := setup(t)
	ctx := context.Background()
	remote, _ := addRemote(t, local, st)
	tariffs, _ := st.Q.ListTariffs(ctx)
	free, _ := users.Create(ctx, domain.CreateInput{Name: "free", TariffID: tariffs[1].ID})
	pinned, _ := users.Create(ctx, domain.CreateInput{Name: "pinned", TariffID: tariffs[1].ID})
	all, _ := st.Q.ListInbounds(ctx)
	first := domain.NodeInbounds(all, LocalNode)[0]
	if _, err := users.Update(ctx, pinned.ID, domain.Patch{Inbounds: &[]int64{first.ID}}); err != nil {
		t.Fatal(err)
	}
	freeSlot, _ := st.Q.GetSlot(ctx, free.SlotID.Int64)
	local.online.Store(&map[string]nodeapi.Online{freeSlot.Name: {IPs: []string{"203.0.113.5"}, Conns: 1}})

	policy := func(s *Syncer) map[int64]nodeapi.Policy {
		t.Helper()
		_, ps, owners, err := s.policies(ctx)
		if err != nil {
			t.Fatal(err)
		}
		by := map[int64]nodeapi.Policy{}
		for _, p := range ps {
			by[owners[p.Slot]] = p
		}
		return by
	}
	here, there := policy(local), policy(remote)
	if p := here[pinned.ID]; !p.Allowed || len(p.Inbounds) != 1 || p.Inbounds[0] != first.Name {
		t.Fatalf("pinned user on its node: %+v", p)
	}
	// An empty list means "all": a user limited to another node's inbounds gets none here.
	if there[pinned.ID].Allowed {
		t.Fatalf("pinned user must not reach the other node: %+v", there[pinned.ID])
	}
	if p := there[free.ID]; !p.Allowed || p.Inbounds != nil || !slices.Equal(p.OtherIPs, []string{"203.0.113.5"}) {
		t.Fatalf("the other node must count the device seen here: %+v", p)
	}
	if p := here[free.ID]; len(p.OtherIPs) != 0 {
		t.Fatalf("a node's own devices are not \"other\": %+v", p)
	}
	if on := local.m.Online(); len(on[freeSlot.Name].IPs) != 1 {
		t.Fatalf("online view: %+v", on)
	}
}

func TestCountersPerNode(t *testing.T) {
	local, n1, st, users, _ := setup(t)
	ctx := context.Background()
	remote, n2 := addRemote(t, local, st)
	tariffs, _ := st.Q.ListTariffs(ctx)
	u, _ := users.Create(ctx, domain.CreateInput{Name: "a", TariffID: tariffs[1].ID})
	slot, _ := st.Q.GetSlot(ctx, u.SlotID.Int64)
	n2.batch = nodeapi.Counters{Epoch: "us", Seq: 5, Slots: map[string]nodeapi.Traffic{slot.Name: {Down: 700}}}
	n1.batch = nodeapi.Counters{Epoch: "nl", Seq: 1, Slots: map[string]nodeapi.Traffic{slot.Name: {Down: 300}}}
	remote.pullCounters(ctx)
	local.pullCounters(ctx)
	remote.pullCounters(ctx) // re-delivered batch
	got, _ := st.Q.GetUser(ctx, u.ID)
	if got.UsedDown != 1000 {
		t.Fatalf("traffic of both nodes adds up once: %d", got.UsedDown)
	}
	if e, s, _ := remote.countersPos(ctx); e != "us" || s != 5 {
		t.Fatalf("remote position %s/%d", e, s)
	}
	if e, s, _ := local.countersPos(ctx); e != "nl" || s != 1 {
		t.Fatalf("local position %s/%d", e, s)
	}
}

// A removed node gets an empty state and its syncer is gone for good, so nothing pushes
// the old listeners back.
func TestRetireStopsTheNode(t *testing.T) {
	local, _, st, _, _ := setup(t)
	ctx := context.Background()
	remote, fake := addRemote(t, local, st)
	if err := local.m.Retire(ctx, remote.id); err != nil {
		t.Fatal(err)
	}
	last := fake.applied[len(fake.applied)-1]
	if len(last.Inbounds) != 0 || len(last.Slots) != 0 {
		t.Fatalf("retired node must be emptied: %+v", last)
	}
	if _, ok := local.m.Syncer(remote.id); ok {
		t.Fatal("the retired node's syncer must be gone")
	}
}

// A bound device has a slot of its own: it gets the user's rules, its traffic counts for
// the user, and the device limit sees the user's devices under every slot.
func TestBoundDeviceSlots(t *testing.T) {
	s, node, st, users, now := setup(t)
	ctx := context.Background()
	tariffs, _ := st.Q.ListTariffs(ctx)
	u, _ := users.Create(ctx, domain.CreateInput{Name: "a", TariffID: tariffs[1].ID})
	clock := func() time.Time { return *now }
	devSlot, err := domain.NewDevices(st, domain.NewPool(st, clock), noopChanges{}, clock).Bind(ctx, u, domain.DeviceInfo{HWID: "phone-0123456789"}, false)
	if err != nil {
		t.Fatal(err)
	}
	own, _ := st.Q.GetSlot(ctx, u.SlotID.Int64)
	s.online.Store(&map[string]nodeapi.Online{own.Name: {IPs: []string{"198.51.100.1"}, Conns: 1}, devSlot.Name: {IPs: []string{"203.0.113.7"}, Conns: 1}})

	_, ps, owners, err := s.policies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]nodeapi.Policy{}
	for _, p := range ps {
		by[p.Slot] = p
	}
	mine, dev := by[own.Name], by[devSlot.Name]
	if owners[devSlot.Name] != u.ID || !dev.Allowed || dev.DeviceLimit != mine.DeviceLimit || dev.QuotaRemaining != mine.QuotaRemaining {
		t.Fatalf("the device's slot gets the user's rules: %+v vs %+v", dev, mine)
	}
	if !slices.Equal(dev.OtherIPs, []string{"198.51.100.1"}) || !slices.Equal(mine.OtherIPs, []string{"203.0.113.7"}) {
		t.Fatalf("each slot counts the user's devices under the other: %v / %v", dev.OtherIPs, mine.OtherIPs)
	}

	node.batch = nodeapi.Counters{Epoch: "e", Seq: 1, Slots: map[string]nodeapi.Traffic{devSlot.Name: {Down: 500}, own.Name: {Down: 100}}}
	s.pullCounters(ctx)
	if got, _ := st.Q.GetUser(ctx, u.ID); got.UsedDown != 600 {
		t.Fatalf("traffic of all the user's slots adds up: %d", got.UsedDown)
	}
}

type noopChanges struct{}

func (noopChanges) PoliciesChanged() {}
func (noopChanges) SlotsChanged()    {}

// The health view names the ports of the state the node runs, so a listener's failure is
// read against the port it failed on, not against a row moved since; a node that runs any
// other state gets none.
func TestHealthCarriesAppliedPorts(t *testing.T) {
	s, node, st, _, _ := setup(t)
	ctx := context.Background()
	s.refreshHealth(ctx)
	if p := s.Health().Ports; p != nil {
		t.Fatalf("nothing applied yet, ports %v", p)
	}
	s.applyState(ctx)
	rev := node.applied[len(node.applied)-1].Revision
	node.health = nodeapi.Health{Revision: rev}
	s.refreshHealth(ctx)
	if p := s.Health().Ports; p["vless-xhttp"] != "443" || p["tuic"] != "8443" {
		t.Fatalf("ports of the applied state: %v", p)
	}
	in, err := st.Q.ListNodeInbounds(ctx, LocalNode)
	if err != nil {
		t.Fatal(err)
	}
	port := "2053"
	if _, _, err := domain.NewInbounds(st, nil, time.Now).Update(ctx, in[0].ID, domain.InboundPatch{Port: &port}); err != nil {
		t.Fatal(err)
	}
	s.refreshHealth(ctx)
	if p := s.Health().Ports; p[in[0].Name] != in[0].Port {
		t.Fatalf("the node still runs the old port until the next apply: %v", p)
	}
	node.health = nodeapi.Health{Revision: rev + 1}
	s.refreshHealth(ctx)
	if p := s.Health().Ports; p != nil {
		t.Fatalf("a state this panel did not apply: ports %v", p)
	}
}
