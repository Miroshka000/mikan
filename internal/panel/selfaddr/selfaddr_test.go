package selfaddr

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"mikan/internal/panel/dnscheck"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/storetest"
)

// panelServer is a panel on TLS that answers the probes of prober and nothing else.
func panelServer(t *testing.T, prober *Prober) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !prober.Serve(w, r) {
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// routed is a client that reaches each public address:port the test names at a local server.
func routed(routes map[string]string) *http.Client {
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		to, ok := routes[addr]
		if !ok {
			return nil, errors.New("no route to " + addr)
		}
		return (&net.Dialer{}).DialContext(ctx, network, to)
	}}
	return &http.Client{Timeout: 5 * time.Second, Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

var (
	oldIP = netip.MustParseAddr("203.0.113.10")
	newIP = netip.MustParseAddr("203.0.113.20")
)

// An address leads here when this panel's keyed answer to its own challenge comes back from
// it, also through a proxy in front of the panel that holds the TLS: a copy of the panel left
// on the old server (the same certificate, another key) answers its own probes only, a server
// that repeats what it is asked cannot answer, and a challenge never handed out gets the
// router's 404.
func TestProber(t *testing.T) {
	ctx := context.Background()
	p := &Prober{}
	srv, other := panelServer(t, p), panelServer(t, &Prober{})
	panelURL, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	front := httputil.NewSingleHostReverseProxy(panelURL)
	front.Transport = srv.Client().Transport
	proxy := httptest.NewTLSServer(front)
	t.Cleanup(proxy.Close)
	echo := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, strings.TrimPrefix(r.URL.Path, Path))
	}))
	t.Cleanup(echo.Close)
	p.Client = routed(map[string]string{
		"203.0.113.20:2053": srv.Listener.Addr().String(),
		"203.0.113.10:2053": other.Listener.Addr().String(),
		"203.0.113.21:443":  proxy.Listener.Addr().String(),
		"203.0.113.30:2053": echo.Listener.Addr().String(),
	})
	if !p.Reaches(ctx, newIP, 2053, "vpn.example.com") || !p.Reaches(ctx, newIP, 2053, "") {
		t.Fatal("this panel's address")
	}
	if !p.Reaches(ctx, netip.MustParseAddr("203.0.113.21"), 443, "vpn.example.com") {
		t.Fatal("this panel behind a proxy that holds the TLS")
	}
	if p.Reaches(ctx, oldIP, 2053, "") {
		t.Fatal("another panel answered for this one")
	}
	if p.Reaches(ctx, netip.MustParseAddr("203.0.113.30"), 2053, "") {
		t.Fatal("a server that repeats the challenge passed for this panel")
	}
	if p.Reaches(ctx, netip.MustParseAddr("203.0.113.40"), 2053, "") {
		t.Fatal("an address that leads nowhere")
	}
	resp, err := srv.Client().Get(srv.URL + Path + "0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a challenge not handed out: %d", resp.StatusCode)
	}
	if len(p.challenges) != 0 {
		t.Fatalf("challenges kept: %v", p.challenges)
	}
}

type env struct {
	t     *testing.T
	set   *settings.Settings
	w     *Watcher
	mu    sync.Mutex
	moves [][2]string
}

func newEnv(t *testing.T, saved string) *env {
	t.Helper()
	ctx := context.Background()
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := &env{t: t, set: settings.New(st.Q)}
	for k, v := range map[string]any{settings.KeyPanelPort: 2053, settings.KeyPublicHost: saved} {
		if err := settings.Set(ctx, e.set, k, v); err != nil {
			t.Fatal(err)
		}
	}
	p := &Prober{}
	srv, old := panelServer(t, p), panelServer(t, &Prober{})
	p.Client = routed(map[string]string{"203.0.113.20:2053": srv.Listener.Addr().String(), "203.0.113.10:2053": old.Listener.Addr().String()})
	e.w = &Watcher{Set: e.set, Prober: p, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: time.Now,
		Moved: func(_ context.Context, from, to string) {
			e.mu.Lock()
			e.moves = append(e.moves, [2]string{from, to})
			e.mu.Unlock()
		}}
	return e
}

func (e *env) check() {
	e.t.Helper()
	if err := e.w.Check(context.Background()); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) saved() string {
	e.t.Helper()
	v, err := e.set.String(context.Background(), settings.KeyPublicHost)
	if err != nil {
		e.t.Fatal(err)
	}
	return v
}

// The server got a new address: the saved one leads to a copy of the panel left on the
// old server, the new one to this panel. Two looks in a row find it, then it is saved and
// told; the next look finds all well.
func TestWatcherMovesTheAddress(t *testing.T) {
	e := newEnv(t, "203.0.113.10")
	e.w.Interfaces = func() []netip.Addr { return []netip.Addr{netip.MustParseAddr("10.0.0.5"), newIP} }
	e.check()
	if st := e.w.Status(); e.saved() != "203.0.113.10" || st.Reaches || st.Found != "203.0.113.20" || len(e.moves) != 0 {
		t.Fatalf("one look moves nothing: %s %+v", e.saved(), st)
	}
	e.check()
	if st := e.w.Status(); e.saved() != "203.0.113.20" || len(e.moves) != 1 || e.moves[0] != [2]string{"203.0.113.10", "203.0.113.20"} || st.MovedFrom != "203.0.113.10" || !st.Reaches {
		t.Fatalf("moved: %s %v %+v", e.saved(), e.moves, e.w.Status())
	}
	e.check()
	if st := e.w.Status(); !st.Reaches || st.Found != "" || len(e.moves) != 1 {
		t.Fatalf("after the move: %+v %v", st, e.moves)
	}
}

// The domain says where the server is now.
func TestWatcherFollowsTheDomain(t *testing.T) {
	doh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ans := map[string]any{"Status": 0, "Answer": []any{}}
		if r.URL.Query().Get("name") == "vpn.example.com" && r.URL.Query().Get("type") == "A" {
			ans["Answer"] = []any{map[string]any{"type": 1, "data": "203.0.113.20"}}
		}
		_ = json.NewEncoder(w).Encode(ans)
	}))
	t.Cleanup(doh.Close)
	e := newEnv(t, "203.0.113.10")
	if err := settings.Set(context.Background(), e.set, settings.KeyDomain, "vpn.example.com"); err != nil {
		t.Fatal(err)
	}
	e.w.DNS = &dnscheck.Checker{Resolvers: []string{doh.URL}, HTTP: http.DefaultClient}
	e.check()
	e.check()
	if e.saved() != "203.0.113.20" {
		t.Fatalf("saved %s", e.saved())
	}
}

// Nothing moves when the saved address leads here, when no other is proved to, when the
// switch is off, the address is a name or a private one.
func TestWatcherLeavesTheAddress(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, "203.0.113.20")
	e.check()
	if st := e.w.Status(); !st.Reaches || e.saved() != "203.0.113.20" {
		t.Fatalf("the address leads here: %+v", st)
	}

	e = newEnv(t, "203.0.113.10")
	for range 3 {
		e.check()
	}
	if e.saved() != "203.0.113.10" || len(e.moves) != 0 || e.w.Status().Found != "" {
		t.Fatalf("no address proved: %s %+v", e.saved(), e.w.Status())
	}

	e.w.Interfaces = func() []netip.Addr { return []netip.Addr{newIP} }
	if err := settings.Set(ctx, e.set, settings.KeyAutoAddress, false); err != nil {
		t.Fatal(err)
	}
	e.check()
	e.check()
	if e.saved() != "203.0.113.10" || !e.w.Status().Unchecked {
		t.Fatalf("switched off: %s %+v", e.saved(), e.w.Status())
	}
	if err := settings.Set(ctx, e.set, settings.KeyAutoAddress, true); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, e.set, settings.KeyPublicHost, "vpn.example.com"); err != nil {
		t.Fatal(err)
	}
	e.check()
	e.check()
	if e.saved() != "vpn.example.com" || !e.w.Status().Unchecked {
		t.Fatalf("a name: %s %+v", e.saved(), e.w.Status())
	}
	if err := settings.Set(ctx, e.set, settings.KeyPublicHost, "192.168.1.10"); err != nil {
		t.Fatal(err)
	}
	e.check()
	e.check()
	if e.saved() != "192.168.1.10" || !e.w.Status().Unchecked {
		t.Fatalf("a private address: %s %+v", e.saved(), e.w.Status())
	}
}
