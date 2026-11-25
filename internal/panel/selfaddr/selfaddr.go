// Package selfaddr keeps the panel's server address (settings.KeyPublicHost) in step with
// the address its server has now. A hoster gives the server a new address, the panel moves
// to another server (mikan move), the domain is pointed at the new address: the saved one
// leads elsewhere then. The panel looks for the address that leads to it now, proves it with
// its keyed answer to a challenge of its own, fetched over its HTTPS port, and saves it in
// place of the old one.
//
// The proof is the answer, not the certificate: after a move the old server may still run a
// copy of the panel with the same certificate, and a new address may have no certificate for
// it yet. The answer is an HMAC under a key that never leaves this process, so neither that
// copy nor a server that repeats what it is asked can give it. A request for a challenge this
// panel did not hand out gets the panel's plain 404, like any other unknown path.
package selfaddr

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"mikan/internal/httpjson"
	"mikan/internal/netaddr"
	"mikan/internal/panel/dnscheck"
	"mikan/internal/panel/settings"
)

// Path is where the panel answers its own probes; the challenge follows it.
const Path = "/.well-known/mikan-self/"

// challengeTTL is how long a probe's challenge is answered.
const challengeTTL = 30 * time.Second

// Prober proves an address leads to this panel: it asks there for the answer to a challenge
// of its own, which only this process can give.
type Prober struct {
	// Client fetches the answer; nil: one that takes any certificate (the answer is the
	// proof, TLS only the way to it) and follows no redirect.
	Client *http.Client

	mu         sync.Mutex
	key        []byte               // the answers' HMAC-SHA256 key, made on first use
	challenges map[string]time.Time // challenge → until when it is answered
}

func (p *Prober) client() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return &http.Client{Timeout: 6 * time.Second,
		Transport:     &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 4 * time.Second},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// Serve answers a probe of this panel and returns true; anything else it leaves to the
// router, which answers it like any path it does not know.
func (p *Prober) Serve(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, Path) {
		return false
	}
	challenge := strings.TrimPrefix(r.URL.Path, Path)
	p.mu.Lock()
	until, ok := p.challenges[challenge]
	key := p.key
	p.mu.Unlock()
	if !ok || time.Now().After(until) {
		return false
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, answer(key, challenge))
	return true
}

// answer is the keyed answer to a challenge, in hex.
func answer(key []byte, challenge string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(challenge))
	return hex.EncodeToString(m.Sum(nil))
}

// present hands out a challenge and returns it with the key that answers it.
func (p *Prober) present() (string, []byte, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", nil, err
	}
	challenge := hex.EncodeToString(b[:])
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.key == nil {
		key := make([]byte, sha256.Size)
		if _, err := rand.Read(key); err != nil {
			return "", nil, err
		}
		p.key = key
	}
	if p.challenges == nil {
		p.challenges = map[string]time.Time{}
	}
	for c, until := range p.challenges {
		if now.After(until) {
			delete(p.challenges, c)
		}
	}
	p.challenges[challenge] = now.Add(challengeTTL)
	return challenge, p.key, nil
}

func (p *Prober) forget(challenge string) {
	p.mu.Lock()
	delete(p.challenges, challenge)
	p.mu.Unlock()
}

// Reaches says whether https://addr:port leads to this panel; sni is the name the request
// goes with ("" for none), for a proxy in front of the panel that routes by it.
func (p *Prober) Reaches(ctx context.Context, addr netip.Addr, port int, sni string) bool {
	challenge, key, err := p.present()
	if err != nil {
		return false
	}
	defer p.forget(challenge)
	u := "https://" + net.JoinHostPort(addr.String(), strconv.Itoa(port)) + Path + challenge
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false
	}
	c := p.client()
	if sni != "" {
		if t, ok := c.Transport.(*http.Transport); ok {
			t = t.Clone()
			t.TLSClientConfig.ServerName = sni
			c = &http.Client{Timeout: c.Timeout, Transport: t, CheckRedirect: c.CheckRedirect}
		}
	}
	resp, err := c.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256))
	return err == nil && resp.StatusCode == http.StatusOK && hmac.Equal(body, []byte(answer(key, challenge)))
}

// AddressStatus is the panel's look at its address, for the admin panel.
type AddressStatus struct {
	CheckedAt time.Time `json:"checked_at,omitzero"`
	// Reaches: the saved address leads to this panel.
	Reaches bool `json:"reaches"`
	// Unchecked: the address is a name, or the look is off: nothing is checked.
	Unchecked bool `json:"unchecked,omitempty"`
	// Found is the address that leads here when the saved one does not.
	Found string `json:"found,omitempty"`
	// MovedFrom and MovedAt: the panel's last change of its address by itself.
	MovedFrom string     `json:"moved_from,omitempty"`
	MovedAt   *time.Time `json:"moved_at,omitempty"`
}

// Watcher looks at the panel's address now and then and saves the one that leads here.
type Watcher struct {
	Set    *settings.Settings
	Prober *Prober
	// DNS asks public DNS where the panel's domain leads; nil: the domain is not asked.
	DNS *dnscheck.Checker
	// Egress is the address the server's connections leave from, as the world sees it; nil:
	// not asked.
	Egress func(ctx context.Context) (netip.Addr, error)
	// Interfaces are the public addresses of the server's own interfaces; nil: none.
	Interfaces func() []netip.Addr
	// Moved hears of a change the watcher saved: the old address and the new one.
	Moved func(ctx context.Context, from, to string)
	Log   *slog.Logger
	Now   func() time.Time

	mu      sync.Mutex
	status  AddressStatus
	strikes int // checks in a row the saved address did not lead here
}

// Every is how often the address is looked at; strikes how many looks in a row must find
// the saved address leading elsewhere before it is replaced: a moment of a busy network
// does not move every link.
var (
	Every   = 5 * time.Minute
	strikes = 2
)

func (w *Watcher) Run(ctx context.Context) {
	t := time.NewTimer(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if err := w.Check(ctx); err != nil && ctx.Err() == nil {
			w.Log.Warn("address: not looked at", "err", err)
		}
		t.Reset(Every)
	}
}

// Status is the last look.
func (w *Watcher) Status() AddressStatus {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.status
}

// Check looks at the address once: when the saved one is an IP address that has led
// elsewhere strikes looks in a row and another one is proved to lead here, that one is saved.
func (w *Watcher) Check(ctx context.Context) error {
	now := w.Now()
	on, err := w.Set.On(ctx, settings.AutoAddress)
	if err != nil {
		return err
	}
	host, err := w.Set.String(ctx, settings.KeyPublicHost)
	if err != nil {
		return err
	}
	// A name follows its DNS; a private address is the admin's own choice (a panel on a
	// network of its own), never moved to a public one.
	saved, perr := netip.ParseAddr(strings.Trim(strings.TrimSpace(host), "[]"))
	if !on || perr != nil || !netaddr.IsPublic(saved) {
		w.setStatus(func(s *AddressStatus) {
			*s = AddressStatus{CheckedAt: now, Unchecked: true, MovedFrom: s.MovedFrom, MovedAt: s.MovedAt}
		})
		w.strike(0)
		return nil
	}
	saved = saved.Unmap()
	port, _, err := settings.Get[int](ctx, w.Set, settings.KeyPanelPort)
	if err != nil || port <= 0 {
		return err
	}
	domain, err := w.Set.String(ctx, settings.KeyDomain)
	if err != nil {
		return err
	}
	if w.Prober.Reaches(ctx, saved, port, domain) {
		w.strike(0)
		w.setStatus(func(s *AddressStatus) { s.CheckedAt, s.Reaches, s.Unchecked, s.Found = now, true, false, "" })
		return nil
	}
	found := w.find(ctx, saved, port, domain)
	n := w.strike(1)
	w.setStatus(func(s *AddressStatus) {
		s.CheckedAt, s.Reaches, s.Unchecked, s.Found = now, false, false, addrString(found)
	})
	if !found.IsValid() || n < strikes {
		return nil
	}
	// Saved only while it is what was looked at: the admin may have typed another meanwhile.
	cur, err := w.Set.String(ctx, settings.KeyPublicHost)
	if err != nil || cur != host {
		return err
	}
	to := found.String()
	if err := settings.Set(ctx, w.Set, settings.KeyPublicHost, to); err != nil {
		return err
	}
	w.Log.Warn("address: the server's address changed, saved by itself", "from", host, "to", to)
	w.strike(0)
	w.setStatus(func(s *AddressStatus) { s.Reaches, s.Found, s.MovedFrom, s.MovedAt = true, "", host, &now })
	if w.Moved != nil {
		w.Moved(ctx, host, to)
	}
	return nil
}

func addrString(a netip.Addr) string {
	if !a.IsValid() {
		return ""
	}
	return a.String()
}

// strike adds n to the looks in a row the saved address led elsewhere (0 starts again) and
// returns them.
func (w *Watcher) strike(n int) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	if n == 0 {
		w.strikes = 0
	} else {
		w.strikes += n
	}
	return w.strikes
}

func (w *Watcher) setStatus(f func(*AddressStatus)) {
	w.mu.Lock()
	f(&w.status)
	w.mu.Unlock()
}

// find is the first address of the saved one's family that leads here: where the domain
// leads, where the server's connections leave from, the server's own interfaces.
func (w *Watcher) find(ctx context.Context, saved netip.Addr, port int, domain string) netip.Addr {
	var cands []netip.Addr
	add := func(as ...netip.Addr) {
		for _, a := range as {
			a = a.Unmap()
			if a.IsValid() && a != saved && a.Is4() == saved.Is4() && netaddr.IsPublic(a) && !slices.Contains(cands, a) {
				cands = append(cands, a)
			}
		}
	}
	if domain != "" && w.DNS != nil {
		if as, err := w.DNS.Lookup(ctx, domain); err == nil {
			add(as...)
		}
	}
	if w.Interfaces != nil {
		add(w.Interfaces()...)
	}
	if w.Egress != nil && saved.Is4() {
		if a, err := w.Egress(ctx); err == nil {
			add(a)
		}
	}
	for _, a := range cands {
		if ctx.Err() != nil {
			break
		}
		if w.Prober.Reaches(ctx, a, port, domain) {
			return a
		}
	}
	return netip.Addr{}
}

// EchoServices tell a client the address it came from, as text; the installer asks the same.
var EchoServices = []string{"https://api.ipify.org", "https://ipv4.icanhazip.com", "https://ifconfig.me/ip"}

// Egress asks the echo services in turn for the IPv4 address the server's connections
// leave from.
func Egress(ctx context.Context) (netip.Addr, error) {
	c := &http.Client{Timeout: 6 * time.Second, Transport: &http.Transport{Proxy: nil, ForceAttemptHTTP2: true,
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 4 * time.Second}).DialContext(ctx, "tcp4", addr)
		}}}
	last := errors.New("no echo service answered")
	for _, u := range EchoServices {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "mikan")
		var body []byte
		if body, err = httpjson.Fetch(c, req, 64); err != nil {
			last = err
			continue
		}
		if a, err := netip.ParseAddr(strings.TrimSpace(string(body))); err == nil && a.Unmap().Is4() {
			return a.Unmap(), nil
		}
	}
	return netip.Addr{}, last
}
