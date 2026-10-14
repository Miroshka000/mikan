// Package panelapi is the panel's API for addons of protocol 2 (docs: PROTOCOL-v2.md in the
// marketplace repository). It has a listener of its own on the loopback address the host
// gave out in state.json, never the public port, and it knows no admin session: an addon
// proves itself with the panel token the host gave it, and may call only what its grant,
// approved by the admin, holds.
package panelapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"mikan/internal/panel/addons"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/events"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
)

// Prefix is where the API lives on its listener; the host hands it to addons as
// MIKAN_PANEL_URL.
const Prefix = "/panel/v2"

// MaxBody bounds what an addon may send in one request.
const MaxBody = 1 << 20

type Deps struct {
	Addons *addons.Manager
	Store  *store.Store // the admin's approvals, kv, events and the bot's buffer
	Hub    *events.Hub
	Log    *slog.Logger
	Now    func() time.Time
	// Settings hold the kv limit and the addon that holds the bot. The Bot API proxy
	// (botproxy.go) also needs the gateway, and where the panel's own pages are (SubBase, for
	// Mini App buttons).
	Settings *settings.Settings
	Telegram Telegram
	SubBase  func(ctx context.Context) string
	// Bot serves the bot's screens running as an addon (tgbot.RPCHandler): POST
	// /panel/v2/bot/{method}, for the addon that holds the bot. nil: not served.
	Bot http.Handler
	// Users are the rules the grants of users:grant go through (days, traffic: the admin's
	// own paths). nil: the routes of users:all and users:grant are not served.
	Users *domain.Users
}

// Caller is the addon a request came from and what it may do.
type Caller struct {
	ID          string
	Version     string
	Permissions []string
}

func (c Caller) Can(perm string) bool { return addons.HasPerm(c.Permissions, perm) }

type callerKey struct{}

// CallerOf is the addon behind a request that passed the server.
func CallerOf(ctx context.Context) (Caller, bool) {
	c, ok := ctx.Value(callerKey{}).(Caller)
	return c, ok
}

// Error is the body of every refusal: the same shape as the adapters' own errors.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Fail answers with an error; message must not carry secrets.
func Fail(w http.ResponseWriter, status int, code, message string) {
	Write(w, status, Error{Code: code, Message: message})
}

// Write answers with JSON.
func Write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Server serves the API; Run keeps its listener on the address the host gave out.
type Server struct {
	d Deps

	mux     *http.ServeMux // the routes, each behind its permission
	updates *events.Hub    // wakes getUpdates of the bot addon
	talk    talking        // the accounts the bot addon is talking to (botrpc.go)
	uploads chan struct{}  // files on their way through the Bot API proxy
	push    pushTune       // the pauses of the push delivery (push.go); zero: the real ones

	mu    sync.Mutex
	addrs map[string]bool // what it listens on: the Host a request must name
}

func New(d Deps) *Server {
	s := &Server{d: d, mux: http.NewServeMux(), updates: events.NewHub(), uploads: make(chan struct{}, 4)}
	s.mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		Fail(w, http.StatusNotFound, "not_found", "no such method")
	})
	s.Handle(http.MethodGet, "/self", "", s.self)
	s.handleKV()
	s.handleEvents()
	if d.Users != nil {
		s.handleUsers()
	}
	if d.Bot != nil {
		s.Handle(http.MethodPost, "/bot/{method}", "bot", func(w http.ResponseWriter, r *http.Request) {
			// The panel's data of the bot is the holder's alone, as its chats are.
			if c, _ := CallerOf(r.Context()); !s.holds(r.Context(), c) {
				Fail(w, http.StatusForbidden, "forbidden", "the addon does not hold the bot")
				return
			}
			if c, _ := CallerOf(r.Context()); !s.guardRPC(w, r, c) {
				return
			}
			d.Bot.ServeHTTP(w, r)
		})
	}
	return s
}

// Handle adds a route below Prefix that needs perm; "" lets any approved addon in. path is a
// ServeMux pattern ("/kv/{key}"). Routes are added before Run.
func (s *Server) Handle(method, path, perm string, h http.HandlerFunc) {
	if perm != "" && !slices.Contains(addons.Permissions, perm) {
		panic("panelapi: unknown permission " + perm)
	}
	s.mux.HandleFunc(method+" "+Prefix+path, func(w http.ResponseWriter, r *http.Request) {
		if c, _ := CallerOf(r.Context()); perm != "" && !c.Can(perm) {
			Fail(w, http.StatusForbidden, "forbidden", "the addon has no permission "+perm)
			return
		}
		h(w, r)
	})
}

// ServeHTTP lets in an addon by its token, then a route its grant allows.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// A browser has no business here: an Origin is a page trying the loopback port.
	if r.Header.Get("Origin") != "" {
		Fail(w, http.StatusForbidden, "forbidden", "not for browsers")
		return
	}
	// The Host must be the address listened on, so a name that resolves to loopback does
	// not let a page in (DNS rebinding).
	s.mu.Lock()
	known := len(s.addrs) == 0 || s.addrs[r.Host]
	s.mu.Unlock()
	if !known {
		Fail(w, http.StatusMisdirectedRequest, "bad_request", "unknown host")
		return
	}
	if strings.HasPrefix(r.URL.Path, "/bot") {
		s.botAPI(w, r)
		return
	}
	c, status, code := s.authenticate(r)
	if status != 0 {
		Fail(w, status, code, code)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxBody)
	s.mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), callerKey{}, c)))
}

// maxPollWait is the longest a long poll (events, getUpdates) waits, in seconds.
const maxPollWait = 50

// intParam reads an integer query value: def when it is empty; ok is false when it is no
// integer within lo..hi.
func intParam(raw string, def, lo, hi int64) (n int64, ok bool) {
	if raw == "" {
		return def, true
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	return n, err == nil && n >= lo && n <= hi
}

// pageSize is a page limit as a query takes it: n within 1..hi, hi at most a page of 1000
// rows. The bound is checked here, next to the conversion, whatever the caller checked.
func pageSize(n, hi int64) int32 {
	n = min(n, hi, 1000)
	// n is at most 1000 already; the constant bound sits by the conversion for the analysers.
	if n < 1 || n > 1000 {
		return 1
	}
	return int32(n)
}

// clampParam reads a parameter as Telegram does: def when it is no integer, else the
// nearest value within lo..hi.
func clampParam(raw string, def, lo, hi int64) int64 {
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return def
	}
	return min(max(n, lo), hi)
}

// longPoll is what both feeds (events, getUpdates) wait with: read until it finds something
// or wait is over, sleeping on the hub between tries. A kick may come before the commit it
// announces, so a try that finds nothing sleeps at most a second, not until the next kick.
// An error is read's, or ctx's when the request ended meanwhile.
func longPoll[T any](ctx context.Context, hub *events.Hub, wait time.Duration, read func() ([]T, error)) ([]T, error) {
	deadline := time.Now().Add(wait)
	for {
		// Taken before the read: what is committed after the read kicks it.
		woken := hub.Wait()
		rows, err := read()
		if err != nil || len(rows) > 0 || !time.Now().Before(deadline) {
			return rows, err
		}
		t := time.NewTimer(min(time.Until(deadline), time.Second))
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-woken:
		case <-t.C:
		}
		t.Stop()
	}
}

// authenticate finds the running addon of protocol 2 the token belongs to, and its grant;
// it answers a status and a code when there is none.
func (s *Server) authenticate(r *http.Request) (Caller, int, string) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		token = ""
	}
	return s.authenticateToken(r.Context(), token)
}

// authenticateToken is authenticate for a token taken from elsewhere (the Bot API proxy's path).
func (s *Server) authenticateToken(ctx context.Context, token string) (Caller, int, string) {
	if token == "" {
		return Caller{}, http.StatusUnauthorized, "unauthorized"
	}
	st, err := s.d.Addons.State()
	if err != nil {
		s.d.Log.Warn("addons api: state", "err", err)
		return Caller{}, http.StatusServiceUnavailable, "unavailable"
	}
	id := ""
	// Every token is compared, so the time does not tell how far one matched.
	for aid, a := range st.Adapters {
		if a.KindOf() != addons.KindPayment && a.PanelToken != "" &&
			subtle.ConstantTimeCompare([]byte(a.PanelToken), []byte(token)) == 1 && a.Status == "running" {
			id = aid
		}
	}
	if id == "" {
		return Caller{}, http.StatusUnauthorized, "unauthorized"
	}
	perms, version, err := addons.LoadGrant(ctx, s.d.Store.Q, id)
	if errors.Is(err, addons.ErrNoGrant) {
		return Caller{}, http.StatusForbidden, "not_approved"
	}
	if err != nil {
		s.d.Log.Warn("addons api: grant", "addon", id, "err", err)
		return Caller{}, http.StatusServiceUnavailable, "unavailable"
	}
	return Caller{ID: id, Version: version, Permissions: perms}, 0, ""
}

// self tells an addon who the panel takes it for and what it may do.
func (s *Server) self(w http.ResponseWriter, r *http.Request) {
	c, _ := CallerOf(r.Context())
	Write(w, http.StatusOK, map[string]any{"id": c.ID, "version": c.Version, "permissions": c.Permissions})
}

// Loopback: an address on 127.0.0.1 with an unprivileged port, as the host gives out.
func Loopback(addr string) bool {
	ap, err := netip.ParseAddrPort(addr)
	return err == nil && ap.Addr() == netip.MustParseAddr("127.0.0.1") && ap.Port() >= 1024
}

// Bridge: the gateway of the host's bridge for custom addons, a private IPv4 address with an
// unprivileged port, as the host gives out.
func Bridge(addr string) bool {
	ap, err := netip.ParseAddrPort(addr)
	return err == nil && ap.Addr().Is4() && ap.Addr().IsPrivate() && ap.Port() >= 1024
}

// wanted is what the host's state asks to be listened on: the loopback address for the
// official addons, and the bridge gateway while a custom addon runs.
func wanted(st addons.State) map[string]bool {
	out := map[string]bool{}
	if Loopback(st.PanelAPI) {
		out[st.PanelAPI] = true
	}
	if Bridge(st.PanelAPIBridge) {
		out[st.PanelAPIBridge] = true
	}
	return out
}

// Run keeps a listener on each address of the host's state: none while no addon of protocol
// 2 was installed, new ones when they change (a restored backup, the bridge coming and going).
func (s *Server) Run(ctx context.Context) {
	servers := map[string]*http.Server{}
	// Run returns only after the requests in flight are done: the database closes after it.
	var wg sync.WaitGroup
	defer wg.Wait()
	stop := func(addr string) {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = servers[addr].Shutdown(sctx)
		cancel()
		delete(servers, addr)
	}
	publish := func() {
		s.mu.Lock()
		s.addrs = map[string]bool{}
		for a := range servers {
			s.addrs[a] = true
		}
		s.mu.Unlock()
	}
	defer func() {
		for a := range servers {
			stop(a)
		}
		publish()
	}()
	failed := map[string]bool{} // addresses that would not bind: told once, tried again quietly
	check := func() {
		st, err := s.d.Addons.State()
		if err != nil {
			return
		}
		want := wanted(st)
		for a := range servers {
			if !want[a] {
				stop(a)
			}
		}
		for a := range want {
			if servers[a] != nil {
				continue
			}
			ln, err := net.Listen("tcp", a)
			if err != nil {
				if !failed[a] {
					s.d.Log.Warn("addons api: listen", "addr", a, "err", err)
				}
				failed[a] = true
				continue
			}
			delete(failed, a)
			srv := &http.Server{Handler: s, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second,
				WriteTimeout: 90 * time.Second, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 16 << 10}
			servers[a] = srv
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
					s.d.Log.Warn("addons api: serve", "err", err)
				}
			}()
			s.d.Log.Info("addons api: listening", "addr", a)
		}
		publish()
	}
	check()
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	prune := time.NewTicker(10 * time.Minute)
	defer prune.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			check()
		case <-prune.C:
			s.pruneKV(ctx)
			s.pruneUpdates(ctx)
			if _, err := events.Prune(ctx, s.d.Store.Q, s.d.Now()); err != nil && ctx.Err() == nil {
				s.d.Log.Warn("addons api: prune events", "err", err)
			}
		}
	}
}
