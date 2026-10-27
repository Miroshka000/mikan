package autotune

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/nodesync"
	"mikan/internal/panel/presets"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/store/storetest"
	"mikan/internal/proto"
)

type fakeNodes struct {
	activity map[int64]nodeapi.Activity
	targets  map[string]nodeapi.TargetResult // by dest; any other dest works
	found    []nodeapi.TargetResult
	checks   int
	onScan   func() // runs while a scan is under way: the admin edits meanwhile
	health   map[int64]nodesync.HealthView
}

func (f *fakeNodes) Health(id int64) (nodesync.HealthView, bool) {
	h, ok := f.health[id]
	return h, ok
}

func (f *fakeNodes) Activity(_ context.Context, id int64) (nodeapi.Activity, error) {
	a, ok := f.activity[id]
	if !ok {
		return a, nodeapi.ErrUnavailable
	}
	return a, nil
}

func (f *fakeNodes) CheckTarget(_ context.Context, _ int64, req nodeapi.TargetCheckRequest) (nodeapi.TargetResult, error) {
	f.checks++
	if r, ok := f.targets[req.Dest]; ok {
		return r, nil
	}
	return good(req.Dest, req.SNI), nil
}

func (f *fakeNodes) ScanTargets(context.Context, int64, nodeapi.TargetScanRequest) (nodeapi.TargetScan, error) {
	if f.onScan != nil {
		f.onScan()
	}
	return nodeapi.TargetScan{Scanned: 253, Results: f.found}, nil
}

func good(dest, sni string) nodeapi.TargetResult {
	return nodeapi.TargetResult{Dest: dest, SNI: sni, OK: true, TLS13: true, H2: true, X25519: true, CertValid: true, DNSMatch: true}
}

type changes struct{ slots int }

func (c *changes) PoliciesChanged() {}
func (c *changes) SlotsChanged()    { c.slots++ }

type env struct {
	ctx   context.Context
	st    *store.Store
	set   *settings.Settings
	tn    *Tuner
	nodes *fakeNodes
	ch    *changes
	now   time.Time
	slot  string
	user  int64

	reaching []string // inbounds the device reaches; nil = no activity
}

// setup: the seeded node 1 (XHTTP 443/tcp, Hysteria2 443/udp, TUIC 8443/udp, Vision
// 8443/tcp) and one user whose profile is up to date.
func setup(t *testing.T) *env {
	t.Helper()
	e := &env{ctx: context.Background(), now: time.Unix(1_800_000_000, 0), ch: &changes{}}
	clock := func() time.Time { return e.now }
	var err error
	if e.st, err = storetest.Open(e.ctx, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.st.Close() })
	if err := domain.Seed(e.ctx, e.st, e.now); err != nil {
		t.Fatal(err)
	}
	e.set = settings.New(e.st.Q)
	for k, v := range map[string]any{settings.KeyPublicHost: "203.0.113.10", settings.KeyDomain: "vpn.example.com", settings.KeyPanelPort: 21355} {
		if err := settings.Set(e.ctx, e.set, k, v); err != nil {
			t.Fatal(err)
		}
	}
	tariffs, err := e.st.Q.ListTariffs(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	u, err := domain.NewUsers(e.st, domain.NewPool(e.st, clock), e.ch, clock).Create(e.ctx, domain.CreateInput{Name: "a", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	slot, err := e.st.Q.GetSlot(e.ctx, u.SlotID.Int64)
	if err != nil {
		t.Fatal(err)
	}
	e.slot, e.user = slot.Name, u.ID
	e.refreshProfile(t)
	e.nodes = &fakeNodes{activity: map[int64]nodeapi.Activity{}, targets: map[string]nodeapi.TargetResult{}}
	e.tn = New(e.st, e.set, e.nodes, e.ch, slog.New(slog.NewTextHandler(io.Discard, nil)), clock, DefaultOptions())
	e.tn.pick = func(int) int { return 0 }
	return e
}

// device is the user's device the node sees; laptop is another of theirs, on another
// network: one network alone is never a block.
const device, laptop = "198.51.100.20", "192.0.2.80"

// refreshProfile: the device fetched the subscription just now.
func (e *env) refreshProfile(t *testing.T) {
	t.Helper()
	for _, ip := range []string{device, laptop} {
		if err := e.st.Q.RecordSubFetch(e.ctx, db.RecordSubFetchParams{UserID: e.user, Ip: ip, FetchedAt: e.now.Unix() + 1}); err != nil {
			t.Fatal(err)
		}
	}
}

// worked: until now the device got through everywhere, and the tuner saw it reach every
// inbound. A device only counts as cut off from an inbound it has reached before.
func (e *env) worked(t *testing.T) {
	t.Helper()
	e.reaches("vless-xhttp", "hysteria2", "tuic", "vless-vision")
	e.step(t, 0)
}

// reaches: from now on the user's device keeps reaching these inbounds (url-test checks
// every few minutes); every step sees them a minute before its time.
func (e *env) reaches(names ...string) {
	e.reaching = names
	e.activity()
}

func (e *env) activity() {
	seen := map[string]int64{}
	for _, n := range e.reaching {
		seen[n] = e.now.Add(-time.Minute).Unix()
	}
	e.nodes.activity[1] = nodeapi.Activity{Clients: []nodeapi.ClientActivity{{Slot: e.slot, IP: device, Seen: seen}, {Slot: e.slot, IP: laptop, Seen: seen}}}
}

func (e *env) step(t *testing.T, d time.Duration) {
	t.Helper()
	e.now = e.now.Add(d)
	if e.reaching != nil {
		e.activity()
	}
	e.tn.Step(e.ctx)
}

func (e *env) inbound(t *testing.T, name string) db.Inbound {
	t.Helper()
	all, err := e.st.Q.ListInbounds(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range all {
		if in.NodeID == 1 && in.Name == name {
			return in
		}
	}
	t.Fatalf("no inbound %s", name)
	return db.Inbound{}
}

func (e *env) events(t *testing.T) []db.InboundEvent {
	t.Helper()
	ev, err := e.st.Q.ListInboundEventsSince(e.ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

func sniOf(t *testing.T, in db.Inbound) string {
	t.Helper()
	tpl, err := proto.Parse(in.Config)
	if err != nil {
		t.Fatal(err)
	}
	_, names := presets.Dest(tpl)
	if len(names) == 0 {
		return ""
	}
	return names[0]
}

func TestMovesABlockedPortAfterHold(t *testing.T) {
	e := setup(t)
	hold := DefaultOptions().Hold
	x := e.inbound(t, "vless-xhttp")

	e.worked(t)
	e.reaches("hysteria2", "tuic", "vless-vision")
	e.step(t, 0)
	s, ok := e.tn.Status(x.ID)
	if !ok || !s.CutOff || s.Blocked != 2 || e.inbound(t, "vless-xhttp").Port != "443" {
		t.Fatalf("cut off, but not moved yet: %+v", s)
	}
	e.reaches("hysteria2", "tuic", "vless-vision")
	e.step(t, hold-time.Minute)
	if e.inbound(t, "vless-xhttp").Port != "443" {
		t.Fatal("moved before the hold time")
	}
	e.reaches("hysteria2", "tuic", "vless-vision")
	e.step(t, time.Minute)
	moved := e.inbound(t, "vless-xhttp")
	if moved.Port != "2053" || moved.Config != x.Config {
		t.Fatalf("moved to %s (the first free pool port is 2053), config kept: %v", moved.Port, moved.Config == x.Config)
	}
	ev := e.events(t)
	if len(ev) != 1 || ev[0].Kind != "port" || ev[0].OldValue != "443" || ev[0].NewValue != "2053" || ev[0].Reason != "blocked" || ev[0].Network != "tcp" {
		t.Fatalf("event: %+v", ev)
	}
	if e.ch.slots == 0 || e.nodes.checks == 0 {
		t.Fatalf("the node must get the change (%d) and the target is checked first (%d)", e.ch.slots, e.nodes.checks)
	}
	if s, _ := e.tn.Status(x.ID); s.CutOff {
		t.Fatalf("state is reset after a move: %+v", s)
	}

	// The device still has the old profile: silence on the new port is no evidence.
	e.reaches("hysteria2", "tuic", "vless-vision")
	e.step(t, hold+time.Minute)
	if s, _ := e.tn.Status(x.ID); s.CutOff {
		t.Fatalf("a stale profile counted: %+v", s)
	}
}

func TestSwitches(t *testing.T) {
	e := setup(t)
	hold := DefaultOptions().Hold
	x := e.inbound(t, "vless-xhttp")
	for _, k := range []string{settings.KeyAutoPort, settings.KeyAutoSNI} {
		if err := settings.Set(e.ctx, e.set, k, false); err != nil {
			t.Fatal(err)
		}
	}
	e.worked(t)
	e.reaches("hysteria2", "tuic", "vless-vision")
	e.step(t, 0)
	e.step(t, hold)
	if s, _ := e.tn.Status(x.ID); !s.CutOff || s.Stuck != "off" || e.inbound(t, "vless-xhttp").Port != "443" || len(e.events(t)) != 0 {
		t.Fatalf("switched off globally: %+v", s)
	}

	// Globally on, but this inbound keeps its port: a REALITY inbound gets another target.
	for _, k := range []string{settings.KeyAutoPort, settings.KeyAutoSNI} {
		if err := settings.Set(e.ctx, e.set, k, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.st.Q.SetInboundAuto(e.ctx, db.SetInboundAutoParams{AutoPort: 0, AutoSni: 1, ID: x.ID}); err != nil {
		t.Fatal(err)
	}
	e.nodes.found = []nodeapi.TargetResult{good("203.0.113.10:443", "vpn.example.com"), good("203.0.113.44:443", "shop.example.org")}
	e.reaches("hysteria2", "tuic", "vless-vision")
	e.step(t, time.Minute)
	got := e.inbound(t, "vless-xhttp")
	if got.Port != "443" || sniOf(t, got) != "shop.example.org" {
		t.Fatalf("port kept, target replaced by a neighbor (never the panel's own name): port %s sni %s", got.Port, sniOf(t, got))
	}
	if ev := e.events(t); len(ev) != 1 || ev[0].Kind != "sni" || ev[0].Reason != "blocked" || ev[0].NewValue != "shop.example.org" {
		t.Fatalf("event: %+v", ev)
	}
}

func TestReplacesADeadTargetOnly(t *testing.T) {
	e := setup(t)
	every := DefaultOptions().CheckEvery
	xhttp, vision := e.inbound(t, "vless-xhttp"), e.inbound(t, "vless-vision")
	dead := presets.DefaultDest
	e.nodes.targets[dead] = nodeapi.TargetResult{Dest: dead, Error: "timeout"}
	e.nodes.found = []nodeapi.TargetResult{good("203.0.113.44:443", "shop.example.org"), good("203.0.113.45:443", "news.example.net")}
	if err := e.st.Q.SetInboundAuto(e.ctx, db.SetInboundAutoParams{AutoPort: 1, AutoSni: 0, ID: vision.ID}); err != nil {
		t.Fatal(err)
	}

	e.step(t, 0)
	e.step(t, every)
	if s, _ := e.tn.Status(xhttp.ID); s.TargetOK || s.TargetError != "timeout" || sniOf(t, e.inbound(t, "vless-xhttp")) != "www.microsoft.com" {
		t.Fatalf("two failed checks: shown, not replaced yet: %+v", s)
	}
	e.step(t, every)
	if got := sniOf(t, e.inbound(t, "vless-xhttp")); got != "shop.example.org" {
		t.Fatalf("third failed check: replaced by the fastest neighbor, got %s", got)
	}
	if got := sniOf(t, e.inbound(t, "vless-vision")); got != "www.microsoft.com" {
		t.Fatalf("Vision has target replacement off: %s", got)
	}
	if s, _ := e.tn.Status(vision.ID); s.TargetOK || s.TargetError != "timeout" {
		t.Fatalf("its target is still checked and shown: %+v", s)
	}
	if ev := e.events(t); len(ev) != 1 || ev[0].Reason != "target_down" || ev[0].OldValue != "www.microsoft.com" {
		t.Fatalf("event: %+v", ev)
	}
	if hy, _ := e.tn.Status(e.inbound(t, "hysteria2").ID); !hy.TargetAt.IsZero() {
		t.Fatal("Hysteria2 has no REALITY target to check")
	}
}

// A dead target behind a proxy is shown, never replaced: the proxy may route by the site.
func TestDeadTargetBehindAProxyStays(t *testing.T) {
	e := setup(t)
	every := DefaultOptions().CheckEvery
	vision := e.inbound(t, "vless-vision")
	dead := presets.DefaultDest
	e.nodes.targets[dead] = nodeapi.TargetResult{Dest: dead, Error: "timeout"}
	e.nodes.found = []nodeapi.TargetResult{good("203.0.113.44:443", "shop.example.org")}
	if err := e.st.Q.SetInboundListen(e.ctx, db.SetInboundListenParams{Listen: "127.0.0.1", ID: vision.ID}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.Q.SetInboundAuto(e.ctx, db.SetInboundAutoParams{AutoPort: 0, AutoSni: 1, ID: vision.ID}); err != nil {
		t.Fatal(err)
	}
	for _, d := range []time.Duration{0, every, every, every} {
		e.step(t, d)
	}
	if got := sniOf(t, e.inbound(t, "vless-vision")); got != "www.microsoft.com" {
		t.Fatalf("replaced behind a proxy: %s", got)
	}
	if s, _ := e.tn.Status(vision.ID); s.TargetOK || s.TargetError != "timeout" {
		t.Fatalf("its target is still checked and shown: %+v", s)
	}
}

// A cut-off inbound whose target is down but has no other site nearby still gets a new
// port: the target was not the only thing to try.
func TestNoOtherTargetStillMovesThePort(t *testing.T) {
	e := setup(t)
	e.nodes.targets[presets.DefaultDest] = nodeapi.TargetResult{Dest: presets.DefaultDest, Error: "timeout"}
	e.nodes.found = []nodeapi.TargetResult{good("203.0.113.10:443", "vpn.example.com")} // only the panel's own name
	e.worked(t)
	e.reaches("hysteria2", "tuic", "vless-vision")
	e.step(t, 0)
	e.step(t, DefaultOptions().Hold)
	x := e.inbound(t, "vless-xhttp")
	if x.Port == "443" || sniOf(t, x) != "www.microsoft.com" {
		t.Fatalf("port %s, sni %s: want a new port and the old target", x.Port, sniOf(t, x))
	}
}

// A port move that does not help: after Escalate the REALITY target goes too, then the
// tuner waits for the cooldown instead of moving on and on.
func TestEscalatesToTheTarget(t *testing.T) {
	e := setup(t)
	o := DefaultOptions()
	x := e.inbound(t, "vless-xhttp")
	e.nodes.found = []nodeapi.TargetResult{good("203.0.113.44:443", "shop.example.org")}
	blocked := func(d time.Duration) {
		e.reaches("hysteria2", "tuic", "vless-vision")
		e.step(t, d)
	}
	e.worked(t)
	blocked(0)
	blocked(o.Hold)
	if e.inbound(t, "vless-xhttp").Port != "2053" {
		t.Fatal("first: the port")
	}
	e.refreshProfile(t)
	blocked(time.Minute)
	blocked(o.Hold)
	if s, _ := e.tn.Status(x.ID); s.Stuck != "waiting" || sniOf(t, e.inbound(t, "vless-xhttp")) != "www.microsoft.com" {
		t.Fatalf("before Escalate the target stays: %+v", s)
	}
	blocked(o.Escalate)
	if got := sniOf(t, e.inbound(t, "vless-xhttp")); got != "shop.example.org" {
		t.Fatalf("then the target: %s", got)
	}
	ev := e.events(t)
	if len(ev) != 2 || ev[1].Kind != "sni" || ev[1].Reason != "still_blocked" {
		t.Fatalf("events: %+v", ev)
	}
	e.refreshProfile(t)
	blocked(time.Minute)
	blocked(o.Hold)
	if s, _ := e.tn.Status(x.ID); s.Stuck != "waiting" || len(e.events(t)) != 2 {
		t.Fatalf("both changed within the cooldown: wait: %+v", s)
	}
}

// Happ does not speak TUIC: a device that checks every link it can use but never got
// through to TUIC does not make TUIC look blocked.
func TestNeverReachedIsNoEvidence(t *testing.T) {
	e := setup(t)
	tuic := e.inbound(t, "tuic")
	e.reaches("vless-xhttp", "hysteria2", "vless-vision")
	e.step(t, 0)
	e.step(t, DefaultOptions().Hold)
	if s, _ := e.tn.Status(tuic.ID); s.CutOff || s.Blocked != 0 || e.inbound(t, "tuic").Port != tuic.Port {
		t.Fatalf("an app that never reached TUIC: %+v", s)
	}
}

// Bound devices have keys of their own: the tuner sees them, judges their profile by their
// own fetch (not by the address), and remembers what they reached across a restart.
func TestBoundDevices(t *testing.T) {
	e := setup(t)
	e.boundPhone(t, visit{profileTo: home})
	if s, _ := e.tn.Status(e.inbound(t, "vless-xhttp").ID); !s.CutOff || s.Blocked != 2 {
		t.Fatalf("the bound device must count, and what it reached must survive a restart: %+v", s)
	}
}

// A bound device is seen as it asks for its keys, before its profile is made: one turned
// away with a notice, or left without a profile by an error, took nothing and never counts.
func TestBoundDeviceWithoutProfile(t *testing.T) {
	for name, visits := range map[string][]visit{
		"no profile": {{}},
		// Past the 5 minutes a known device is written down at most once in: its last visit
		// moves on, and it took nothing then.
		"turned away after a profile": {{profileTo: home}, {after: 6 * time.Minute}},
		"profile to another address":  {{profileTo: "192.0.2.99"}},
	} {
		t.Run(name, func(t *testing.T) {
			e := setup(t)
			e.boundPhone(t, visits...)
			if s, _ := e.tn.Status(e.inbound(t, "vless-xhttp").ID); s.CutOff || s.Blocked != 0 {
				t.Fatalf("a bound device that took no profile counted: %+v", s)
			}
		})
	}
}

// home is where the bound phone asks for its keys from.
const home = "198.51.100.30"

// visit: after a pause, the phone asks for its keys from home, and its user's profile goes
// out to profileTo ("" = the phone got none).
type visit struct {
	after     time.Duration
	profileTo string
}

// boundPhone: a bound device makes its visits; then it reaches everything, and after a
// restart all but XHTTP.
func (e *env) boundPhone(t *testing.T, visits ...visit) {
	t.Helper()
	clock := func() time.Time { return e.now }
	u, err := e.st.Q.GetUser(e.ctx, e.user)
	if err != nil {
		t.Fatal(err)
	}
	var phone db.Slot
	for _, v := range visits {
		e.now = e.now.Add(v.after)
		if phone, err = domain.NewDevices(e.st, domain.NewPool(e.st, clock), e.ch, clock).Bind(e.ctx, u, domain.DeviceInfo{HWID: "phone-0123456789", App: "Happ/3.4.1", IP: home}, false); err != nil {
			t.Fatal(err)
		}
		if v.profileTo != "" {
			if err := e.st.Q.RecordSubFetch(e.ctx, db.RecordSubFetchParams{UserID: e.user, Ip: v.profileTo, FetchedAt: e.now.Unix()}); err != nil {
				t.Fatal(err)
			}
		}
	}
	act := func(names ...string) {
		seen := map[string]int64{}
		for _, n := range names {
			seen[n] = e.now.Add(-time.Minute).Unix()
		}
		// Addresses the user never fetched the subscription from.
		e.nodes.activity[1] = nodeapi.Activity{Clients: []nodeapi.ClientActivity{{Slot: phone.Name, IP: "192.0.2.7", Seen: seen}, {Slot: phone.Name, IP: "203.0.113.70", Seen: seen}}}
	}
	act("vless-xhttp", "hysteria2", "tuic", "vless-vision")
	e.step(t, 0)

	e.tn = New(e.st, e.set, e.nodes, e.ch, slog.New(slog.NewTextHandler(io.Discard, nil)), clock, DefaultOptions())
	e.now = e.now.Add(time.Minute)
	act("hysteria2", "tuic", "vless-vision")
	e.tn.Step(e.ctx)
}

// Behind a proxy the port is what the proxy forwards to, and the site may be what it
// routes by: a blocked inbound there never moves nor changes its site, even with the
// switches left on (a row from before they were locked).
func TestNeverMovesAPortBehindAProxy(t *testing.T) {
	e := setup(t)
	x := e.inbound(t, "vless-xhttp")
	if err := e.st.Q.SetInboundListen(e.ctx, db.SetInboundListenParams{Listen: "127.0.0.1", ID: x.ID}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.Q.SetInboundAuto(e.ctx, db.SetInboundAutoParams{AutoPort: 1, AutoSni: 0, ID: x.ID}); err != nil {
		t.Fatal(err)
	}
	hold := DefaultOptions().Hold
	e.worked(t)
	for _, d := range []time.Duration{0, hold, hold} {
		e.reaches("hysteria2", "tuic", "vless-vision")
		e.step(t, d)
	}
	if s, _ := e.tn.Status(x.ID); !s.CutOff || s.Stuck != "off" || e.inbound(t, "vless-xhttp").Port != "443" || len(e.events(t)) != 0 {
		t.Fatalf("moved behind a proxy: %+v, port %s", s, e.inbound(t, "vless-xhttp").Port)
	}

	if err := e.st.Q.SetInboundAuto(e.ctx, db.SetInboundAutoParams{AutoPort: 1, AutoSni: 1, ID: x.ID}); err != nil {
		t.Fatal(err)
	}
	sni := sniOf(t, e.inbound(t, "vless-xhttp"))
	e.nodes.found = []nodeapi.TargetResult{good("203.0.113.44:443", "shop.example.org")}
	e.reaches("hysteria2", "tuic", "vless-vision")
	e.step(t, time.Minute)
	if got := e.inbound(t, "vless-xhttp"); got.Port != "443" || sniOf(t, got) != sni || len(e.events(t)) != 0 {
		t.Fatalf("changed behind a proxy: port %s sni %s (was %s)", got.Port, sniOf(t, got), sni)
	}
}

// The subscription port is the panel's: a blocked inbound skips it on the way out.
func TestMoveSkipsTheSubscriptionPort(t *testing.T) {
	e := setup(t)
	if err := settings.Set(e.ctx, settings.New(e.st.Q), settings.KeySubPort, 2053); err != nil {
		t.Fatal(err)
	}
	hold := DefaultOptions().Hold
	e.worked(t)
	for _, d := range []time.Duration{0, hold} {
		e.reaches("hysteria2", "tuic", "vless-vision")
		e.step(t, d)
	}
	if got := e.inbound(t, "vless-xhttp").Port; got != "2083" {
		t.Fatalf("moved to %s, want the next pool port after the subscription port", got)
	}
}
