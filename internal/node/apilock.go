package node

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"mikan/internal/hostchan"
	"mikan/internal/netaddr"
	"mikan/internal/nodeapi"
)

// The node's API for its panel alone (DesiredState.APIAllow). Scanners find the API port
// like any other; the panel's pinned certificate keeps them out, but they still reach the
// TLS handshake. With the lock on, the node refuses their connections in its own listener
// (the first line, which needs nothing of the host) and asks its server's firewall for the
// same rule (hostchan apilock.go), so they do not reach the node at all.
//
// The panel's addresses are what the panel sent plus what it reached the node from: a
// connection the panel's certificate opened is the panel's, whatever its address, so a
// panel behind NAT or with a second address is not locked out by the panel's own guess.
// And when the panel stays away for nodeapi.APILockStale (it moved to an address nobody
// knew), both the listener and the host's rule let everyone in again, the panel gets in
// with its new address, and the rule comes back with it. A panel with a domain is let in
// sooner: the node looks up the host of the panel's address (DesiredState.PanelURL) once a
// minute, and the addresses it leads to are the panel's too.

const (
	// apiLockFrom is how many of the addresses the panel came from are kept: its IPv4 and
	// IPv6 ones, and one it moved from.
	apiLockFrom = 4
	// apiSeenSave is how often the panel's contact reaches the disk: the host only needs it
	// to the minute, and the panel asks every few seconds.
	apiSeenSave = time.Minute
	// apiHostSilent is how long the host may take to put the rule in before the panel is
	// told it did not: its timer looks every minute.
	apiHostSilent = 3 * time.Minute
	// panelLookupEvery is how often the host of the panel's address is looked up.
	panelLookupEvery = time.Minute
)

// apiLock is the lock's state; the zero allow (nil) is off.
type apiLock struct {
	ch  *hostchan.Channel
	log *slog.Logger
	now func() time.Time

	mu    sync.Mutex
	allow []netip.Prefix // what the panel sent; nil: off
	seen  time.Time      // when the panel last reached the API; zero: not since the lock was lifted
	from  []netip.Addr   // the addresses it came from, newest first
	saved time.Time      // when seen last reached the disk
	// asked is what the host was last asked for ("off", or the networks joined), askedAt
	// when; "" while it is not known (the host may hold a rule of before).
	asked   string
	askedAt time.Time
	// panelHost is the host of the panel's address; panelAddrs what it leads to, looked up
	// at lookedAt. resolve looks a name up; tests replace it.
	panelHost  string
	panelAddrs []netip.Addr
	lookedAt   time.Time
	resolve    func(ctx context.Context, host string) ([]netip.Addr, error)
}

func newAPILock(ch *hostchan.Channel, log *slog.Logger, now func() time.Time) *apiLock {
	l := &apiLock{ch: ch, log: log, now: now, asked: "off", resolve: func(ctx context.Context, host string) ([]netip.Addr, error) {
		return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	}}
	if s, ok := ch.ReadAPILockSeen(); ok {
		if s.At > 0 {
			l.seen = time.Unix(s.At, 0)
		}
		for _, f := range s.From {
			if a, err := netip.ParseAddr(f); err == nil && len(l.from) < apiLockFrom {
				l.from = append(l.from, a.Unmap())
			}
		}
	}
	// A rule of before stays in the host's firewall over a restart of the node: until the
	// state says whether it is still wanted, the host's word is not taken for "off".
	if st, ok := ch.ReadAPILockStatus(); ok && st.On {
		l.asked = ""
	}
	return l
}

// set takes the networks of a new state; nil turns the lock off. A network that does not
// parse is dropped (the panel checks them; a newer panel may send what this node cannot read).
func (l *apiLock) set(allow []string) {
	var nets []netip.Prefix
	if allow != nil {
		nets = []netip.Prefix{}
		for _, s := range allow {
			p, err := netip.ParsePrefix(s)
			if err != nil {
				l.log.Warn("api lock: not a network", "network", s)
				continue
			}
			nets = append(nets, p.Masked())
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.allow = nets
	l.sync()
}

// setPanel takes the panel's address of a new state: the addresses its host leads to are
// let in. An address literal is taken at once, a name is looked up by tick.
func (l *apiLock) setPanel(panelURL string) {
	host := ""
	if u, err := url.Parse(panelURL); err == nil && u.Scheme == "https" {
		host = strings.ToLower(u.Hostname())
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if host == l.panelHost {
		return
	}
	l.panelHost, l.panelAddrs, l.lookedAt = host, nil, time.Time{}
	if a, err := netip.ParseAddr(host); err == nil && netaddr.IsPublic(a) {
		l.panelAddrs = []netip.Addr{a.Unmap()}
	}
	l.sync()
}

// lookUpPanel looks the host of the panel's address up again once panelLookupEvery: a
// panel that moved with its domain gets in as soon as the name leads to its new address.
// What was known stays when the lookup fails.
func (l *apiLock) lookUpPanel() {
	l.mu.Lock()
	host, due := l.panelHost, l.now().Sub(l.lookedAt) >= panelLookupEvery
	l.mu.Unlock()
	if host == "" || !due {
		return
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	found, err := l.resolve(ctx, host)
	cancel()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.panelHost != host {
		return
	}
	l.lookedAt = l.now()
	if err != nil {
		return
	}
	var addrs []netip.Addr
	for _, a := range found {
		// Only the internet: a name that led into the node's own network would open the API to it.
		if a = a.Unmap(); netaddr.IsPublic(a) && !slices.Contains(addrs, a) {
			addrs = append(addrs, a)
		}
	}
	slices.SortFunc(addrs, func(a, b netip.Addr) int { return a.Compare(b) })
	if !slices.Equal(addrs, l.panelAddrs) {
		l.panelAddrs = addrs
		l.sync()
	}
}

// active says whether strangers are refused now: the lock is on and the panel came lately.
// Call with mu held.
func (l *apiLock) active(now time.Time) bool {
	return l.allow != nil && !l.seen.IsZero() && now.Sub(l.seen) < nodeapi.APILockStale
}

// admit says whether a connection from a may reach the API.
func (l *apiLock) admit(a netip.Addr) bool {
	a = a.Unmap()
	if a.IsLoopback() {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.active(l.now()) {
		return true
	}
	if slices.Contains(l.from, a) || slices.Contains(l.panelAddrs, a) {
		return true
	}
	for _, p := range l.allow {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// contact notes a request the panel's certificate opened, from remote ("ip:port").
func (l *apiLock) contact(remote string) {
	ap, err := netip.ParseAddrPort(remote)
	if err != nil {
		return
	}
	a := ap.Addr().Unmap()
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	back := !l.active(now)
	l.seen = now
	known := slices.Contains(l.from, a)
	if i := slices.Index(l.from, a); i != 0 {
		if i > 0 {
			l.from = slices.Delete(l.from, i, i+1)
		}
		l.from = slices.Insert(l.from, 0, a)
		if len(l.from) > apiLockFrom {
			l.from = l.from[:apiLockFrom]
		}
	}
	if !known || back || now.Sub(l.saved) >= apiSeenSave {
		l.save(now)
	}
	if l.allow != nil && (back || !known) {
		l.sync()
	}
}

// tick runs every few seconds: it lifts the lock when the host's admin did, and takes the
// rule away from the host when the panel stayed away.
func (l *apiLock) tick() {
	l.lookUpPanel()
	lifted := l.ch.TakeAPILifted()
	l.mu.Lock()
	defer l.mu.Unlock()
	if lifted {
		l.log.Warn("api lock lifted on the server (mikan api-lockdown off): the API answers any address until the panel comes back")
		l.seen = time.Time{}
		l.save(l.now())
	}
	l.sync()
}

// save writes the panel's contact for the host. Call with mu held.
func (l *apiLock) save(now time.Time) {
	s := hostchan.APILockSeen{}
	if !l.seen.IsZero() {
		s.At = l.seen.Unix()
	}
	for _, a := range l.from {
		s.From = append(s.From, a.String())
	}
	if err := l.ch.WriteAPILockSeen(s); err != nil && !errors.Is(err, hostchan.ErrUnavailable) {
		l.log.Error("api lock: record the panel's contact", "err", err)
		return
	}
	l.saved = now
}

// wanted is the rule the host should hold now: nil while the lock is not active. Call with
// mu held.
func (l *apiLock) wanted(now time.Time) []string {
	if !l.active(now) {
		return nil
	}
	nets := make([]string, 0, len(l.allow)+len(l.from))
	for _, p := range l.allow {
		nets = append(nets, p.String())
	}
	for _, a := range slices.Concat(l.from, l.panelAddrs) {
		nets = append(nets, netip.PrefixFrom(a, a.BitLen()).String())
	}
	slices.Sort(nets)
	return slices.Compact(nets)
}

// sync asks the host for the rule when it is not what the host was last asked for. Call
// with mu held.
func (l *apiLock) sync() {
	now := l.now()
	nets := l.wanted(now)
	want := "off"
	if nets != nil {
		want = strings.Join(nets, ",")
	}
	if want == l.asked {
		return
	}
	switch err := l.ch.RequestAPILock(nets); {
	case errors.Is(err, hostchan.ErrUnavailable):
	case err != nil:
		l.log.Error("api lock: ask the host", "err", err)
		return
	case nets == nil && l.allow != nil:
		l.log.Warn("api lock: the panel has not come for a while, the API answers any address until it does", "away", nodeapi.APILockStale)
	case nets == nil:
		l.log.Info("api lock: off")
	default:
		l.log.Info("api lock: only the panel reaches the API", "networks", want)
	}
	l.asked, l.askedAt = want, now
}

// status is how the lock went on the host, for Health; nil while it is off.
func (l *apiLock) status() *nodeapi.PortStatus {
	l.mu.Lock()
	on, askedAt, now := l.allow != nil, l.askedAt, l.now()
	l.mu.Unlock()
	if !on {
		return nil
	}
	st, ok := l.ch.ReadAPILockStatus()
	switch {
	case ok && !st.OK:
		return &nodeapi.PortStatus{Error: st.Error}
	case ok && st.At >= askedAt.Unix(), askedAt.IsZero(), now.Sub(askedAt) < apiHostSilent:
		return &nodeapi.PortStatus{OK: true}
	default:
		return &nodeapi.PortStatus{Error: "the server did not put the firewall rule in: run mikan update on the node's server"}
	}
}

// apiListener refuses the connections admit does not let in: they are closed as soon as
// they are accepted, before a byte of TLS.
type apiListener struct {
	net.Listener
	admit func(netip.Addr) bool
}

// APIListener is ln with the API lock in front: the node's TCP API for its panel.
func (e *Engine) APIListener(ln net.Listener) net.Listener {
	return &apiListener{Listener: ln, admit: e.apiLock().admit}
}

func (l *apiListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if ap, err := netip.ParseAddrPort(c.RemoteAddr().String()); err != nil || l.admit(ap.Addr()) {
			return c, nil
		}
		_ = c.Close()
	}
}

// apiLock is the engine's lock, made on first use.
func (e *Engine) apiLock() *apiLock {
	e.lockOnce.Do(func() { e.lock = newAPILock(e.hostChan(), e.log, time.Now) })
	return e.lock
}

// PanelContact notes a request that came over the panel's TLS, from remote ("ip:port").
func (e *Engine) PanelContact(remote string) { e.apiLock().contact(remote) }

// PanelSeen is when the panel last reached the node over its TLS; zero before it did.
func (e *Engine) PanelSeen() time.Time {
	l := e.apiLock()
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.seen
}

// PanelURL is the panel's address its last state named, the one saved before a restart
// until the panel sends one; "" while none named it.
func (e *Engine) PanelURL() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.appliedAny {
		return e.applied.PanelURL
	}
	if e.saved != nil && e.saved.ok {
		return e.saved.state.PanelURL
	}
	return ""
}

// TickAPILock is called every few seconds (see apiLock.tick).
func (e *Engine) TickAPILock() { e.apiLock().tick() }
