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
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/hub/executor"
	"github.com/metacubex/mihomo/listener"
	mlog "github.com/metacubex/mihomo/log"
	"github.com/metacubex/mihomo/tunnel"

	"mikan/internal/acmechallenge"
	"mikan/internal/fsutil"
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
}

type Engine struct {
	dataDir      string
	home         string
	log          *slog.Logger
	version      string
	started      time.Time
	allowPrivate bool

	Reg *Registry
	tun *Tunnel
	// Challenge answers the ACME challenges the panel orders for the node's address, on
	// port 80 while one is pending.
	Challenge *acmechallenge.Server

	mu         sync.Mutex // serializes Apply
	savedShape string     // policyShape of the policies in the state file
	savedAt    time.Time  // when the state file was written
	applied    nodeapi.DesiredState
	cert       proto.Cert // node certificate files written by the last Apply
	listeners  map[string]nodeapi.ListenerStatus

	errsMu sync.Mutex
	errs   map[string]string // listener name → last listen error
	marker chan string

	sys  *sysSampler
	host hostPortsCache

	routes string // routesKey of what tunnel's proxies and rules hold now
	warpMu sync.Mutex
	warp   nodeapi.WarpStatus // the last check, kept for a minute
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
	e.warpMu.Lock()
	defer e.warpMu.Unlock()
	if !e.warp.CheckedAt.IsZero() {
		age := time.Since(e.warp.CheckedAt)
		if age < warpCacheTTL && (!force || age < warpForceEvery) {
			return e.warp
		}
	}
	check := e.warpCheck
	if check == nil {
		check = probe
	}
	e.warp = check(ctx, warpProxy, endpoint)
	e.logWarp(e.warp, endpoint)
	return e.warp
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
		dataDir: dataDir, home: home, log: log, version: version, started: time.Now(), allowPrivate: o.AllowPrivate,
		listeners: map[string]nodeapi.ListenerStatus{},
		errs:      map[string]string{}, marker: make(chan string, 8), sys: newSysSampler(),
		Challenge: acmechallenge.New(cmp.Or(o.ACMEListen, acmechallenge.DefaultListen)),
	}
	go e.pumpLogs()

	cs, err := loadCounters(filepath.Join(dataDir, countersFile), log)
	if err != nil {
		return nil, err
	}
	e.Reg = NewRegistry(cs.Epoch, cs.Seq, o.DeviceRelease, time.Now)
	e.tun = &Tunnel{inner: tunnel.Tunnel, reg: e.Reg}

	base, _, err := buildConfig(nodeapi.DesiredState{}, proto.Cert{}, o.AllowPrivate)
	if err != nil {
		return nil, err
	}
	cfg, err := executor.ParseWithBytes(base)
	if err != nil {
		return nil, fmt.Errorf("base config: %w", err)
	}
	// ApplyConfig patches listeners with the stock tunnel and drops any others, so it
	// runs exactly once, with no listeners; ours are patched separately in Apply.
	cfg.Listeners = map[string]C.InboundListener{}
	executor.ApplyConfig(cfg, true)

	st, ok, err := loadState(filepath.Join(dataDir, stateFile), log)
	if err != nil {
		return nil, err
	}
	if ok {
		if _, err := e.Apply(st); err != nil {
			log.Error("restore saved state", "err", err)
		}
	}
	e.restoreCounters(cs)
	go e.sys.run()
	go e.Reg.RunShaper(context.Background())
	return e, nil
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
	raw, rejected, err := buildConfig(st, cert, e.allowPrivate)
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
		e.warpMu.Unlock()
	}

	e.Reg.SetSlots(st.Slots)
	e.Reg.SetShaping(st.Shaping)
	e.Reg.SetPolicies(st.Epoch, st.Policies)
	e.Reg.SetShared(sharedListeners(st))
	e.Reg.SetTorrent(st.Torrent)
	e.Reg.SetIngress(st.Filters)
	pools := map[string]string{}
	for _, in := range st.Inbounds {
		if in.Pool != "" {
			pools[in.Name] = in.Pool
		}
	}
	e.Reg.SetPools(pools)

	recreated := withoutRejected(changedInbounds(e.applied, st), rejected)
	e.errsMu.Lock()
	for name := range cfg.Listeners {
		delete(e.errs, name)
	}
	e.errsMu.Unlock()
	listener.PatchInboundListeners(cfg.Listeners, e.tun, true)
	e.syncLogs()

	e.errsMu.Lock()
	failed := map[string]C.InboundListener{}
	statuses := make([]nodeapi.ListenerStatus, 0, len(cfg.Listeners))
	for name, l := range cfg.Listeners {
		ls := nodeapi.ListenerStatus{Name: name, OK: true}
		if msg, bad := e.errs[name]; bad {
			ls = listenFailed(name, msg)
		} else {
			failed[name] = l
		}
		statuses = append(statuses, ls)
	}
	hasFailures := len(failed) != len(cfg.Listeners)
	e.errsMu.Unlock()
	statuses = append(statuses, rejected...)
	// A listener that failed to bind stays registered under its old instance in mihomo
	// and would be skipped as "unchanged" next time; re-patching without it forgets it.
	if hasFailures {
		listener.PatchInboundListeners(failed, e.tun, true)
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Name < statuses[j].Name })

	e.applied = st
	e.listeners = map[string]nodeapi.ListenerStatus{}
	for _, s := range statuses {
		e.listeners[s.Name] = s
	}
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
	if e.allowPrivate || proto.PublicHost(host) {
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
	e.mu.Lock()
	ls := make([]nodeapi.ListenerStatus, 0, len(e.listeners))
	for _, s := range e.listeners {
		ls = append(ls, s)
	}
	rev := e.applied.Revision
	e.mu.Unlock()
	sort.Slice(ls, func(i, j int) bool { return ls[i].Name < ls[j].Name })
	return nodeapi.Health{
		Version: e.version, Core: "mihomo " + mihomoVersion(), Revision: rev, StartedAt: e.started,
		Listeners: ls, Conns: e.Reg.ConnCount(), System: e.sys.last(), Update: e.UpdateStatus(), Host: e.host.get(), Time: time.Now(),
	}
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

// pumpLogs forwards mihomo's log stream: warnings and errors go to our logger,
// listener bind errors are remembered for the listener status.
func (e *Engine) pumpLogs() {
	sub := mlog.Subscribe()
	for ev := range sub {
		msg := ev.Payload
		if strings.HasPrefix(msg, markerPrefix) {
			select {
			case e.marker <- msg:
			default:
			}
			continue
		}
		if ev.LogLevel < mlog.WARNING || connLine(msg) {
			continue
		}
		if name, rest, ok := parseListenErr(msg); ok {
			e.errsMu.Lock()
			e.errs[name] = rest
			e.errsMu.Unlock()
		}
		if ev.LogLevel >= mlog.ERROR {
			e.log.Error("mihomo", "msg", msg)
		} else {
			e.log.Warn("mihomo", "msg", msg)
		}
	}
}

const markerPrefix = "mikan-sync "

// syncLogs waits until every log event emitted before this call has been processed:
// mihomo delivers events in order, so seeing our own marker is a barrier.
func (e *Engine) syncLogs() {
	id := make([]byte, 8)
	_, _ = rand.Read(id)
	want := markerPrefix + hex.EncodeToString(id)
	mlog.Warnln("%s", want)
	timeout := time.After(2 * time.Second)
	for {
		select {
		case got := <-e.marker:
			if got == want {
				return
			}
		case <-timeout:
			e.log.Warn("log barrier timed out; listener errors may be missing")
			return
		}
	}
}

// connLine reports per-connection lines ("[TCP] 1.2.3.4:5 --> host:443 error: ..."). They
// carry user IPs and destinations, so they never reach our logs, even as warnings.
func connLine(msg string) bool {
	return strings.HasPrefix(msg, "[TCP] ") || strings.HasPrefix(msg, "[UDP] ")
}

func parseListenErr(msg string) (name, reason string, ok bool) {
	const p = "Listener "
	const mid = " listen err: "
	if !strings.HasPrefix(msg, p) {
		return "", "", false
	}
	name, reason, ok = strings.Cut(msg[len(p):], mid)
	return name, reason, ok
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

func withoutRejected(names []string, rejected []nodeapi.ListenerStatus) []string {
	if len(rejected) == 0 {
		return names
	}
	bad := make(map[string]bool, len(rejected))
	for _, r := range rejected {
		bad[r.Name] = true
	}
	return slices.DeleteFunc(names, func(n string) bool { return bad[n] })
}

func changedInbounds(prev, next nodeapi.DesiredState) []string {
	old := map[string]string{}
	for _, in := range prev.Inbounds {
		raw, _ := json.Marshal(in)
		old[in.Name] = string(raw)
	}
	slotsChanged := !sameSlots(prev.Slots, next.Slots)
	var out []string
	for _, in := range next.Inbounds {
		raw, _ := json.Marshal(in)
		if o, ok := old[in.Name]; !ok || o != string(raw) || slotsChanged {
			out = append(out, in.Name)
		}
	}
	return out
}

func sameSlots(a, b []nodeapi.Slot) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func mihomoVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, m := range bi.Deps {
			if m.Path == "github.com/metacubex/mihomo" {
				return m.Version
			}
		}
	}
	return "unknown"
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
	e.warpMu.Lock()
	defer e.warpMu.Unlock()
	if r, ok := e.probes[proxy]; ok && time.Since(r.CheckedAt) < time.Minute {
		return r, true
	}
	r := probe(ctx, proxy, "")
	if e.probes == nil {
		e.probes = map[string]nodeapi.ProbeResult{}
	}
	e.probes[proxy] = r
	return r, true
}
