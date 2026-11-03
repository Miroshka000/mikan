package node

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/metacubex/mihomo/component/eventbus"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/hub/executor"
	"github.com/metacubex/mihomo/listener"
	mlog "github.com/metacubex/mihomo/log"
	"github.com/metacubex/mihomo/tunnel"
	"github.com/metacubex/mihomo/tunnel/statistic"

	"mikan/internal/acmechallenge"
	"mikan/internal/frontpage"
	"mikan/internal/fsutil"
	"mikan/internal/hostchan"
	"mikan/internal/logbuf"
	"mikan/internal/netaddr"
	"mikan/internal/nodeapi"
	"mikan/internal/proto"
	"mikan/internal/scan"
)

const (
	stateFile    = "node-state.json"
	countersFile = "node-counters.json"
)

type Options struct {
	DataDir string
	Version string
	Log     *slog.Logger
	// A device (source IP) keeps its place for this long after its last connection closed.
	DeviceRelease time.Duration
	// AllowPrivate lets VPN users reach private and loopback networks of the server.
	AllowPrivate bool
	// ACMEListen is where the panel's HTTP-01 challenges for this node are answered
	// (acmechallenge.ParseListen); "" is port 80.
	ACMEListen string
	// Logs holds the last lines of the node's log for GET /v1/logs; nil: none to show.
	Logs *logbuf.Buffer
}

type Engine struct {
	dataDir      string
	home         string
	log          *slog.Logger
	version      string
	started      time.Time
	allowPrivate bool
	logs         *logbuf.Buffer

	// hostCh is the channel to the units on the node's server (see hostChan).
	hostOnce sync.Once
	hostCh   *hostchan.Channel
	// lock keeps the TCP API for the panel alone (apilock.go); made on first use.
	lockOnce sync.Once
	lock     *apiLock

	Reg *Registry
	tun *Tunnel
	// Challenge answers the ACME challenges the panel orders for the node's address, on
	// port 80 while one is pending.
	Challenge *acmechallenge.Server
	redirect  atomic.Pointer[nodeapi.PortStatus] // the 301 on port 80; nil: off
	// The SNI router on 443 (sni.go) and how its port went; nil: none.
	routerMu     sync.Mutex
	router       *sniRouter
	routerAddr   string
	routerStatus atomic.Pointer[nodeapi.PortStatus]
	// front is the page the router gives strangers on the node's own names (front.go).
	front *frontPage

	mu         sync.Mutex // serializes Apply
	savedShape string     // policyShape of the policies in the state file
	savedAt    time.Time  // when the state file was written
	applied    nodeapi.DesiredState
	// saved is the state from before the restart until Restore takes it; appliedAny says a
	// state was applied since the start (the panel's, or that one).
	saved      *savedState
	appliedAny bool
	cert       proto.Cert // node certificate files written by the last Apply
	listeners  map[string]nodeapi.ListenerStatus
	// shown is what Health reports of the last Apply, published at its end: Health never
	// waits on mu, which a long Apply (or a big SetPolicies) holds, and the panel, which
	// asks every few seconds with a short timeout, does not take the node for down.
	shown atomic.Pointer[shownState]

	sys  *sysSampler
	host hostPortsCache
	mem  memoryLog // only TickMemory touches it

	routes  string             // routesKey of what tunnel's proxies and rules hold now
	exitDNS string             // exitDNSKey of the sniffer and resolver mihomo holds now
	probeMu sync.Mutex         // one outbound probe at a time
	warpMu  sync.Mutex         // the kept results below
	warpGen int                // moves when a new config drops the kept results
	warp    nodeapi.WarpStatus // the last check, kept for a minute
	// warpState is what the log last said about WARP: "ok" or the error code; empty
	// before the first check of a config.
	warpState string
	warpCheck func(ctx context.Context, proxy, endpoint string) nodeapi.WarpStatus // probe, unless a test swaps it
	probes    map[string]nodeapi.ProbeResult                                       // the same for the exits to other nodes
}

// routesKey covers what the outbound side of the config depends on.
func routesKey(st nodeapi.DesiredState, allowPrivate bool) string {
	raw, _ := json.Marshal(struct {
		W *nodeapi.Warp
		E []nodeapi.Exit
		R []string
	}{st.Warp, st.Exits, rules(st, allowPrivate)})
	return string(raw)
}

const (
	warpCacheTTL   = time.Minute
	warpForceEvery = 5 * time.Second // a forced check is let through this often
)

// WarpStatus checks the internet through WARP, at most once a minute. force asks for a
// new check at once, but not more often than every few seconds: the admin's button must
// not turn the node into a way to hammer Cloudflare.
func (e *Engine) WarpStatus(ctx context.Context, force bool) nodeapi.WarpStatus {
	e.mu.Lock()
	configured := e.applied.Warp != nil
	endpoint := ""
	if configured {
		endpoint = e.applied.Warp.Endpoint
	}
	e.mu.Unlock()
	if !configured {
		return nodeapi.WarpStatus{}
	}
	// One probe at a time; warpMu guards only the kept result, so an Apply that resets it
	// (under mu) never waits out a probe of up to 16 seconds.
	e.probeMu.Lock()
	defer e.probeMu.Unlock()
	e.warpMu.Lock()
	if !e.warp.CheckedAt.IsZero() {
		age := time.Since(e.warp.CheckedAt)
		if age < warpCacheTTL && (!force || age < warpForceEvery) {
			defer e.warpMu.Unlock()
			return e.warp
		}
	}
	gen := e.warpGen
	e.warpMu.Unlock()
	check := e.warpCheck
	if check == nil {
		check = probe
	}
	st := check(ctx, warpProxy, endpoint)
	e.warpMu.Lock()
	defer e.warpMu.Unlock()
	if gen == e.warpGen { // not of a config replaced meanwhile
		e.warp = st
		e.logWarp(st, endpoint)
	}
	return st
}

// logWarp writes a failed WARP check to the log once per change (ok to failed, failed
// to ok, another reason), not once per probe: the panel polls every cycle.
func (e *Engine) logWarp(s nodeapi.WarpStatus, endpoint string) {
	state := "ok"
	if !s.OK {
		state = s.Error
		if state == "" {
			state = "failed"
		}
	}
	prev := e.warpState
	e.warpState = state
	switch {
	case state == prev:
	case state != "ok":
		e.log.Warn("WARP check failed", "reason", state, "detail", s.Detail, "endpoint", endpoint)
	case prev != "":
		e.log.Info("WARP works again", "ip", s.IP, "colo", s.Colo)
	}
}

func Start(o Options) (*Engine, error) {
	dataDir, version, log := o.DataDir, o.Version, o.Log
	if o.DeviceRelease <= 0 {
		o.DeviceRelease = 60 * time.Second
	}
	home := filepath.Join(dataDir, "mihomo")
	if err := os.MkdirAll(filepath.Join(home, "tls"), 0o700); err != nil {
		return nil, err
	}
	// Certificates must live under mihomo's home, otherwise its safe-path check rejects them.
	C.SetHomeDir(home)

	e := &Engine{
		dataDir: dataDir, home: home, log: log, version: version, started: time.Now(), allowPrivate: o.AllowPrivate, logs: o.Logs,
		listeners: map[string]nodeapi.ListenerStatus{},
		sys:       newSysSampler(),
		Challenge: acmechallenge.New(cmp.Or(o.ACMEListen, acmechallenge.DefaultListen)),
	}
	go e.pumpLogs()

	cs, err := loadCounters(filepath.Join(dataDir, countersFile), log)
	if err != nil {
		return nil, err
	}
	// The core lists an address of a user this long after its last connection closed: a
	// device keeps its place as long.
	statistic.DefaultManager.Users().IPRelease = o.DeviceRelease
	e.Reg = NewRegistry(cs.Epoch, cs.Seq, o.DeviceRelease, time.Now, citrus{})
	e.tun = &Tunnel{inner: tunnel.Tunnel, reg: e.Reg}
	reg := e.Reg
	statistic.DefaultManager.SetOnLeave(func(c statistic.Tracker) {
		i := c.Info()
		reg.left(i.Metadata, i.UploadTotal.Load(), i.DownloadTotal.Load())
	})

	if err := applyBase(); err != nil {
		return nil, err
	}
	e.exitDNS = exitDNSKey(nil)

	st, ok, err := loadState(filepath.Join(dataDir, stateFile), log)
	if err != nil {
		return nil, err
	}
	e.saved = &savedState{state: st, ok: ok, counters: cs}
	go e.sys.run()
	go e.Reg.Run(context.Background())
	return e, nil
}

var (
	baseOnce sync.Once
	baseErr  error
)

// applyBase gives the core its general settings, once per process: ApplyConfig patches
// listeners with the stock tunnel and drops any others, so it runs with no listeners and
// ours are patched separately in Apply; the way out and the DNS come with every Apply.
func applyBase() error {
	baseOnce.Do(func() {
		base, _, err := buildConfig(nodeapi.DesiredState{}, proto.Cert{}, false, "")
		if err != nil {
			baseErr = err
			return
		}
		cfg, err := executor.ParseWithBytes(base)
		if err != nil {
			baseErr = fmt.Errorf("base config: %w", err)
			return
		}
		cfg.Listeners = map[string]C.InboundListener{}
		executor.ApplyConfig(cfg, true)
	})
	return baseErr
}

// savedState is what the node had before its restart, brought back by Restore.
type savedState struct {
	state    nodeapi.DesiredState
	ok       bool
	counters counterState
}

// Restore brings back the state and the counters saved before the restart. It runs once
// the API is up: listeners that take long to start (a port to free, a WARP endpoint to
// resolve) must not keep the panel out for half a minute, and a state the panel sent in
// the meantime is newer than the saved one, which is then left out. Only the first call
// does anything.
func (e *Engine) Restore() {
	e.mu.Lock()
	saved := e.saved
	e.saved = nil
	if saved != nil && saved.ok && !e.appliedAny {
		if _, err := e.apply(saved.state); err != nil {
			e.log.Error("restore saved state", "err", err)
		}
	}
	e.mu.Unlock()
	if saved != nil {
		e.restoreCounters(saved.counters)
	}
}

// restoreCounters brings back what the slots had counted before the restart. Apply ran
// first, so the quotas it set leave that out: they are set again with the counters in
// place, or users close to their limit would get the traffic counted since the last push
// on top of it.
func (e *Engine) restoreCounters(cs counterState) {
	e.Reg.restore(cs)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.Reg.SetPolicies(e.applied.Epoch, e.applied.Policies)
}

// Apply renders the desired state into mihomo listeners. Unchanged listeners keep their
// connections; policies and slots are updated in place.
func (e *Engine) Apply(st nodeapi.DesiredState) (nodeapi.ApplyResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.apply(st)
}

// apply is Apply with e.mu held.
func (e *Engine) apply(st nodeapi.DesiredState) (nodeapi.ApplyResult, error) {
	var cert proto.Cert
	if st.TLS != nil && st.TLS.CertPEM != "" {
		cert = proto.Cert{CertPath: filepath.Join(e.home, "tls", "node.crt"), KeyPath: filepath.Join(e.home, "tls", "node.key")}
		if err := fsutil.WriteFileAtomic(cert.CertPath, []byte(st.TLS.CertPEM), 0o600); err != nil {
			return nodeapi.ApplyResult{}, err
		}
		if err := fsutil.WriteFileAtomic(cert.KeyPath, []byte(st.TLS.KeyPEM), 0o600); err != nil {
			return nodeapi.ApplyResult{}, err
		}
	}
	e.cert = cert
	// The front page as a file for the inbounds on an HTTP transport (fallback-page); the
	// router's names get it from the page held in memory (front.go).
	front := ""
	if page := frontpage.Page(st.FrontPage); page != nil {
		front = filepath.Join(e.home, "front", frontpage.File(st.FrontPage))
		if err := os.MkdirAll(filepath.Dir(front), 0o755); err != nil {
			return nodeapi.ApplyResult{}, err
		}
		if err := fsutil.WriteFileAtomic(front, page, 0o644); err != nil {
			return nodeapi.ApplyResult{}, err
		}
	}
	raw, rejected, err := buildConfig(st, cert, e.allowPrivate, front)
	if err != nil {
		return nodeapi.ApplyResult{}, &nodeapi.Error{Code: "invalid_state", Message: err.Error()}
	}
	cfg, err := executor.ParseWithBytes(raw)
	if err != nil {
		return nodeapi.ApplyResult{}, &nodeapi.Error{Code: "invalid_state", Message: err.Error()}
	}

	// Listeners are patched below; the way out (WARP and the rules that pick it) is
	// swapped only when it changed, so open connections keep their outbound otherwise.
	if key := routesKey(st, e.allowPrivate); key != e.routes {
		// The old tunnel's WireGuard device would keep its socket and goroutines.
		if old, ok := tunnel.Proxies()[warpProxy]; ok {
			defer func() {
				if c, ok := old.Adapter().(io.Closer); ok {
					_ = c.Close()
				}
			}()
		}
		tunnel.UpdateProxies(cfg.Proxies, cfg.Providers)
		tunnel.UpdateRules(cfg.Rules, cfg.SubRules, cfg.RuleProviders)
		e.routes = key
		e.warpMu.Lock()
		e.warp = nodeapi.WarpStatus{}
		e.warpState = ""
		e.probes = nil
		e.warpGen++
		e.warpMu.Unlock()
	}

	if key := exitDNSKey(st.DNS); key != e.exitDNS {
		if err := applyExitDNS(cfg); err != nil {
			e.log.Error("exit dns", "err", err)
		} else {
			e.exitDNS = key
		}
	}

	e.Reg.SetSlots(st.Slots)
	e.Reg.SetShaping(st.Shaping)
	e.Reg.SetPolicies(st.Epoch, st.Policies)
	e.Reg.SetShared(sharedListeners(st))
	e.Reg.SetTorrent(st.Torrent)
	e.Reg.SetIngress(st.Filters)
	e.setRedirect(st.HTTPRedirect)
	e.apiLock().set(st.APIAllow)
	e.Reg.SetDetail(st.Detail)
	pools := map[string]string{}
	for _, in := range st.Inbounds {
		if in.Pool != "" {
			pools[in.Name] = in.Pool
		}
	}
	e.Reg.SetPools(pools)
	e.Reg.SetInbounds(userListeners(st))

	// The core gives a listener whose users alone changed the new users in place, so the
	// connections of everyone else stay; only what it started anew counts as recreated.
	failed, started := e.patchListeners(cfg.Listeners)
	listening := map[string]C.InboundListener{}
	statuses := make([]nodeapi.ListenerStatus, 0, len(cfg.Listeners))
	for name, l := range cfg.Listeners {
		ls := nodeapi.ListenerStatus{Name: name, OK: true}
		if msg, bad := failed[name]; bad {
			ls = listenFailed(name, msg)
		} else {
			listening[name] = l
		}
		statuses = append(statuses, ls)
	}
	statuses = append(statuses, rejected...)
	// A listener that failed to bind stays registered under its old, closed instance in
	// the core and would be skipped as "unchanged" next time; re-patching without it
	// forgets it.
	if len(listening) != len(cfg.Listeners) {
		listener.PatchInboundListeners(listening, e.tun, true)
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Name < statuses[j].Name })
	ports := make(map[string]string, len(st.Inbounds))
	for _, in := range st.Inbounds {
		ports[in.Name] = in.Port
	}
	if e.front == nil {
		e.front = newFrontPage()
	}
	e.front.set(st.FrontPage, st.TLS)
	e.setRouter(st.Router, ports)
	recreated := make([]string, 0, len(started))
	for name := range started {
		recreated = append(recreated, name)
	}
	sort.Strings(recreated)

	e.applied, e.appliedAny = st, true
	e.listeners = map[string]nodeapi.ListenerStatus{}
	for _, s := range statuses {
		e.listeners[s.Name] = s
	}
	e.shown.Store(&shownState{listeners: statuses, revision: st.Revision})
	if err := e.saveState(st); err != nil {
		e.log.Error("save state", "err", err)
	}
	return nodeapi.ApplyResult{Revision: st.Revision, Recreated: recreated, Listeners: statuses}, nil
}

// Validate parses one inbound with mihomo's own parser without applying it, so the
// panel can refuse a template before it replaces a working listener.
func (e *Engine) Validate(req nodeapi.ValidateRequest) error {
	e.mu.Lock()
	cert := e.cert
	e.mu.Unlock()
	in := req.Inbound
	in.Name = "mikan-validate"
	probe := []nodeapi.Slot{{Name: "validate", UUID: "00000000-0000-4000-8000-000000000000", Secret: "validate"}}
	l, err := listenerFor(in, probe, cert, proto.Options{SelfStealPort: req.SelfStealPort})
	if err != nil {
		return err
	}
	_, err = listener.ParseListener(l)
	return err
}

// TargetAllowed says whether the node may test dest as a REALITY target for the panel:
// public sites, the panel's own port next to it (self-steal), anything in test setups.
func (e *Engine) TargetAllowed(dest string) bool {
	host, port, err := net.SplitHostPort(dest)
	if err != nil || host == "" {
		return false
	}
	if e.allowPrivate || netaddr.PublicHost(host) {
		return true
	}
	e.mu.Lock()
	self := e.applied.SelfStealPort
	e.mu.Unlock()
	return self > 0 && (host == "127.0.0.1" || host == "localhost") && port == strconv.Itoa(self)
}

// quotaSaveEvery is how often a push that changed only the quotas left reaches the disk:
// the panel sends those every half minute and a node that restarts gets fresh ones from
// it at once, so there is little to lose and a fsync of the whole state to save.
const quotaSaveEvery = time.Minute

// TargetOptions is where the node connects when it tests a target: the internet, and the
// panel's own port on loopback. A name that leads anywhere else is refused after it is
// resolved (TargetAllowed only reads the text).
func (e *Engine) TargetOptions() scan.Options {
	e.mu.Lock()
	defer e.mu.Unlock()
	return scan.Options{Any: e.allowPrivate, LoopbackPort: e.applied.SelfStealPort}
}

func (e *Engine) SetPolicies(req nodeapi.PoliciesRequest) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.Reg.SetPolicies(req.Epoch, req.Policies)
	shape := policyShape(req.Policies)
	changed := shape != e.savedShape || req.Epoch != e.applied.Epoch
	e.applied.Policies = req.Policies
	e.applied.Epoch = req.Epoch
	if !changed && time.Since(e.savedAt) < quotaSaveEvery {
		return
	}
	if err := e.saveState(e.applied); err != nil {
		e.log.Error("save state", "err", err)
	}
}

// policyShape is who may do what, without the quotas left: those change with every byte.
func policyShape(ps []nodeapi.Policy) string {
	h := sha256.New()
	enc := json.NewEncoder(h)
	for _, p := range ps {
		pools := make([]string, 0, len(p.Pools))
		for _, q := range p.Pools {
			pools = append(pools, q.Pool+strconv.FormatBool(q.Remaining < 0))
		}
		_ = enc.Encode([]any{p.Slot, p.Allowed, p.Inbounds, p.DeviceLimit, p.QuotaRemaining < 0, p.OtherIPs, pools,
			p.TorrentExempt, p.BannedUntil, p.SpeedMbps, p.Group})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (e *Engine) Health() nodeapi.Health {
	ls, rev := []nodeapi.ListenerStatus{}, int64(0)
	if sh := e.shown.Load(); sh != nil {
		ls, rev = append(ls, sh.listeners...), sh.revision
	}
	return nodeapi.Health{
		Version: e.version, Core: coreVersion(), Revision: rev, StartedAt: e.started,
		Listeners: ls, Conns: e.Reg.ConnCount(), System: e.sys.last(), Update: e.UpdateStatus(), Host: e.host.get(), Time: time.Now(),
		Redirect: e.redirect.Load(), Router: e.routerStatus.Load(), APILock: e.apiLock().status(),
	}
}

// setRedirect turns the 301 on port 80 on or off and keeps how it went for Health. A port
// another program holds is tried again at the next Apply.
func (e *Engine) setRedirect(on bool) {
	if !on {
		_ = e.Challenge.SetRedirect(false)
		e.redirect.Store(nil)
		return
	}
	st := &nodeapi.PortStatus{OK: true}
	if err := e.Challenge.SetRedirect(true); err != nil {
		st = &nodeapi.PortStatus{Error: err.Error()}
		e.log.Warn("http redirect", "err", err)
	}
	e.redirect.Store(st)
}

// shownState is the last Apply as Health reports it: the listeners sorted by name.
type shownState struct {
	listeners []nodeapi.ListenerStatus
	revision  int64
}

// PersistCounters is called periodically and on shutdown; at most the last interval
// of traffic is lost if the process dies.
func (e *Engine) PersistCounters() error {
	raw, err := json.Marshal(e.Reg.snapshot())
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(filepath.Join(e.dataDir, countersFile), raw, 0o600)
}

func (e *Engine) saveState(st nodeapi.DesiredState) error {
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if err := fsutil.WriteFileAtomic(filepath.Join(e.dataDir, stateFile), raw, 0o600); err != nil {
		return err
	}
	e.savedShape, e.savedAt = policyShape(st.Policies), time.Now()
	return nil
}

// pumpLogs forwards the core's warnings and errors to our logger.
func (e *Engine) pumpLogs() {
	sub := mlog.Subscribe()
	for ev := range sub {
		msg := ev.Payload
		if ev.LogLevel < mlog.WARNING || connLine(msg) {
			continue
		}
		if ev.LogLevel >= mlog.ERROR {
			e.log.Error("core", "msg", msg)
		} else {
			e.log.Warn("core", "msg", msg)
		}
	}
}

// inboundStates is the mask of the core's listener events.
var inboundStates = func() eventbus.Mask {
	m, err := eventbus.ParseTypes(eventbus.TypeInboundState)
	if err != nil {
		panic(err)
	}
	return m
}()

// patchListeners puts ls in place of the running listeners and reports what the core did:
// the listeners that failed to listen, with the error, and those it started anew (not the
// ones that kept running, users updated in place). The core publishes the events of a
// patch while it runs, so all of them are in the subscription once it returns.
func (e *Engine) patchListeners(ls map[string]C.InboundListener) (failed map[string]string, started map[string]bool) {
	// Each listener gives at most a stop and a start or failure; old ones dropped, a stop.
	sub := eventbus.Subscribe(inboundStates, 4*len(ls)+64)
	defer sub.Close()
	listener.PatchInboundListeners(ls, e.tun, true)
	failed, started = map[string]string{}, map[string]bool{}
	for {
		select {
		case ev := <-sub.C():
			st, ok := ev.Data.(eventbus.InboundState)
			if !ok {
				continue
			}
			switch st.State {
			case eventbus.InboundFailed:
				failed[st.Inbound] = st.Error
				delete(started, st.Inbound)
			case eventbus.InboundStarted:
				started[st.Inbound] = true
				delete(failed, st.Inbound)
			}
		default:
			if n := sub.TakeDropped(); n > 0 {
				e.log.Warn("listener events lost; listener errors may be missing", "dropped", n)
			}
			return failed, started
		}
	}
}

// connLine reports per-connection lines ("[TCP] 1.2.3.4:5 --> host:443 error: ..."). They
// carry user IPs and destinations, so they never reach our logs, even as warnings.
func connLine(msg string) bool {
	return strings.HasPrefix(msg, "[TCP] ") || strings.HasPrefix(msg, "[UDP] ")
}

// listenFailed is the status of a listener that could not bind. A port another program
// holds is named by a code, not left to the OS's wording: the panel moves such an inbound.
func listenFailed(name, msg string) nodeapi.ListenerStatus {
	ls := nodeapi.ListenerStatus{Name: name, Error: msg}
	if nodeapi.AddrInUse(msg) {
		ls.Code = nodeapi.ListenerAddrInUse
	}
	return ls
}

// coreVersion names the network core the node is built with: Citrus, which go.mod puts in
// place of mihomo, by its release tag or, for a commit of its dev branch, the commit.
func coreVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, m := range bi.Deps {
			if m.Path == "github.com/metacubex/mihomo" {
				return coreName(m)
			}
		}
	}
	return "unknown"
}

func coreName(m *debug.Module) string {
	if m.Replace == nil {
		return "mihomo " + m.Version
	}
	v := m.Replace.Version
	// A pseudo-version (v1.19.33-0.20261018104129-2cae038ea2a8) says nothing but its commit.
	if parts := strings.Split(v, "-"); len(parts) >= 3 && len(parts[len(parts)-1]) == 12 {
		v = parts[len(parts)-1][:7]
	}
	return "citrus " + v
}

// loadCounters reads the counters file. A missing or broken one starts a new counter
// epoch, which the panel takes as a node with a fresh volume and re-bases its quotas.
func loadCounters(path string, log *slog.Logger) (counterState, error) {
	fresh := func() counterState {
		id := make([]byte, 8)
		_, _ = rand.Read(id)
		return counterState{Epoch: hex.EncodeToString(id)}
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return fresh(), nil
	}
	if err != nil {
		return counterState{}, err
	}
	var cs counterState
	if err := json.Unmarshal(raw, &cs); err != nil || cs.Epoch == "" {
		if err == nil {
			err = errors.New("no epoch")
		}
		if err := setAside(path, err, log); err != nil {
			return counterState{}, err
		}
		return fresh(), nil
	}
	return cs, nil
}

// loadState reads the saved desired state; ok is false when there is none to apply: no
// file, or a broken one, which is kept aside. The panel then pushes the whole state, as
// it does to any node that reports revision 0.
func loadState(path string, log *slog.Logger) (st nodeapi.DesiredState, ok bool, err error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return st, false, nil
	}
	if err != nil {
		return st, false, err
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		if err := setAside(path, err, log); err != nil {
			return nodeapi.DesiredState{}, false, err
		}
		return nodeapi.DesiredState{}, false, nil
	}
	return st, true, nil
}

// setAside renames a file the node cannot read to <name>.corrupt-<time>, so the node
// starts clean instead of failing on it at every restart, and the evidence stays.
func setAside(path string, why error, log *slog.Logger) error {
	to := path + ".corrupt-" + time.Now().UTC().Format("20060102T150405")
	if err := os.Rename(path, to); err != nil {
		return fmt.Errorf("%s is broken (%v) and cannot be moved aside: %w", filepath.Base(path), why, err)
	}
	log.Error("a saved file is broken: starting without it", "file", filepath.Base(path), "kept_as", filepath.Base(to), "err", why)
	return nil
}

// Probe checks the internet through one outbound of the running config, at most once a
// minute per outbound: only WARP and the exits to other nodes may be asked about.
func (e *Engine) Probe(ctx context.Context, proxy string) (nodeapi.ProbeResult, bool) {
	if proxy == warpProxy {
		return e.WarpStatus(ctx, false), true
	}
	e.mu.Lock()
	known := false
	for _, x := range e.applied.Exits {
		known = known || x.Name == proxy
	}
	e.mu.Unlock()
	if !known {
		return nodeapi.ProbeResult{}, false
	}
	e.probeMu.Lock()
	defer e.probeMu.Unlock()
	e.warpMu.Lock()
	if r, ok := e.probes[proxy]; ok && time.Since(r.CheckedAt) < time.Minute {
		defer e.warpMu.Unlock()
		return r, true
	}
	gen := e.warpGen
	e.warpMu.Unlock()
	r := probe(ctx, proxy, "")
	e.warpMu.Lock()
	defer e.warpMu.Unlock()
	if gen == e.warpGen {
		if e.probes == nil {
			e.probes = map[string]nodeapi.ProbeResult{}
		}
		e.probes[proxy] = r
	}
	return r, true
}
