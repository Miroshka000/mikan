package panelapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/addons"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/store/storetest"
)

// grants are the approvals a world starts with, by addon.
type grants map[string]db.AddonGrant

var (
	botToken = strings.Repeat("b", 48)
	payToken = strings.Repeat("p", 48)
	newToken = strings.Repeat("n", 48)
)

// world is a data directory with the host's state and a server over it, with a real
// database holding the approvals g.
func world(t *testing.T, st addons.State, g grants) (*Server, string) {
	t.Helper()
	ctx := context.Background()
	store, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	for id, x := range g {
		if err := store.Q.UpsertAddonGrant(ctx, db.UpsertAddonGrantParams{AddonID: id, Version: x.Version, Permissions: x.Permissions, ApprovedAt: 1}); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	writeState(t, dir, st)
	m := addons.New(dir, "", "dev", slog.New(slog.DiscardHandler), time.Now)
	return New(Deps{Addons: m, Store: store, Settings: settings.New(store.Q), Log: slog.New(slog.DiscardHandler), Now: time.Now}), dir
}

func writeState(t *testing.T, dir string, st addons.State) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "addons"), 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(st)
	if err := os.WriteFile(filepath.Join(dir, "addons", "state.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func state() addons.State {
	return addons.State{Adapters: map[string]addons.Installed{
		"telegram": {Protocol: addons.Protocol2, Kind: addons.KindBot, PanelToken: botToken, Token: "t", Status: "running"},
		"fresh":    {Protocol: addons.Protocol2, Kind: addons.KindBot, PanelToken: newToken, Token: "t", Status: "running"},
		"broken":   {Protocol: addons.Protocol2, Kind: addons.KindBot, PanelToken: strings.Repeat("x", 48), Token: "t", Status: "stopped"},
		// A payment adapter has no panel token; one planted in its entry opens nothing.
		"yookassa": {PanelToken: payToken, Token: "t", Status: "running"},
	}}
}

func call(s http.Handler, method, path, token string, header map[string]string) (int, Error, map[string]any) {
	r := httptest.NewRequest(method, "http://127.0.0.1:41990"+path, nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range header {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	var e Error
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &e)
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return w.Code, e, body
}

// An addon is let in by its own token and its grant, and reaches only what the grant holds.
func TestAccess(t *testing.T) {
	g := grants{
		"telegram": {AddonID: "telegram", Version: "1.0.0", Permissions: `["bot","kv"]`},
		"yookassa": {AddonID: "yookassa", Version: "1.0.0", Permissions: `["users:write"]`},
		"broken":   {AddonID: "broken", Version: "1.0.0", Permissions: `["users:write"]`},
	}
	s, _ := world(t, state(), g)
	ok := func(w http.ResponseWriter, r *http.Request) {
		c, _ := CallerOf(r.Context())
		Write(w, http.StatusOK, map[string]string{"caller": c.ID})
	}
	s.Handle(http.MethodPost, "/kv/x", "kv", ok)
	s.Handle(http.MethodPost, "/users/1/grant", "users:write", ok)

	cases := []struct {
		name, method, path, token string
		header                    map[string]string
		status                    int
		code                      string
	}{
		{"no token", "GET", "/panel/v2/self", "", nil, 401, "unauthorized"},
		{"a wrong token", "GET", "/panel/v2/self", strings.Repeat("z", 48), nil, 401, "unauthorized"},
		{"a payment adapter's planted token", "POST", "/panel/v2/users/1/grant", payToken, nil, 401, "unauthorized"},
		{"a stopped addon", "POST", "/panel/v2/users/1/grant", strings.Repeat("x", 48), nil, 401, "unauthorized"},
		{"not approved", "GET", "/panel/v2/self", newToken, nil, 403, "not_approved"},
		{"a permission it lacks", "POST", "/panel/v2/users/1/grant", botToken, nil, 403, "forbidden"},
		{"a browser", "GET", "/panel/v2/self", botToken, map[string]string{"Origin": "https://evil.example"}, 403, "forbidden"},
		{"another host name", "GET", "/panel/v2/self", botToken, map[string]string{"Host": "rebound.example:41990"}, 421, "bad_request"},
		{"no such method", "GET", "/panel/v2/nothing", botToken, nil, 404, "not_found"},
		{"the right method, the wrong verb", "GET", "/panel/v2/kv/x", botToken, nil, 404, "not_found"},
	}
	s.mu.Lock()
	s.addrs = map[string]bool{"127.0.0.1:41990": true}
	s.mu.Unlock()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := c.header
			r := httptest.NewRequest(c.method, "http://127.0.0.1:41990"+c.path, nil)
			if c.token != "" {
				r.Header.Set("Authorization", "Bearer "+c.token)
			}
			for k, v := range h {
				if k == "Host" {
					r.Host = v
				} else {
					r.Header.Set(k, v)
				}
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			var e Error
			_ = json.Unmarshal(w.Body.Bytes(), &e)
			if w.Code != c.status || e.Code != c.code {
				t.Fatalf("got %d %q, want %d %q", w.Code, e.Code, c.status, c.code)
			}
		})
	}

	status, _, body := call(s, "POST", "/panel/v2/kv/x", botToken, nil)
	if status != 200 || body["caller"] != "telegram" {
		t.Fatalf("an allowed call: %d %v", status, body)
	}
	status, _, body = call(s, "GET", "/panel/v2/self", botToken, nil)
	if status != 200 || body["id"] != "telegram" || fmt.Sprint(body["permissions"]) != "[bot kv]" {
		t.Fatalf("self: %d %v", status, body)
	}
}

// A route with a permission nobody can be granted is a programming error, caught at once.
func TestUnknownPermission(t *testing.T) {
	s, _ := world(t, state(), grants{})
	defer func() {
		if recover() == nil {
			t.Fatal("a route with an unknown permission was taken")
		}
	}()
	s.Handle(http.MethodGet, "/x", "admin", nil)
}

// The listener follows the address in the host's state: none without one, only a loopback
// one, a new one when it changes, and none after Run returns.
func TestListener(t *testing.T) {
	free := func() string {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		return ln.Addr().String()
	}
	st := state()
	s, dir := world(t, st, grants{"telegram": {AddonID: "telegram", Permissions: `[]`}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()

	listening := func() string {
		s.mu.Lock()
		defer s.mu.Unlock()
		for a := range s.addrs {
			return a
		}
		return ""
	}
	wait := func(want string) {
		t.Helper()
		for start := time.Now(); time.Since(start) < 10*time.Second; time.Sleep(50 * time.Millisecond) {
			if listening() == want {
				return
			}
		}
		t.Fatalf("listens on %q, want %q", listening(), want)
	}
	wait("")

	st.PanelAPI = "0.0.0.0:41990"
	writeState(t, dir, st)
	time.Sleep(6 * time.Second)
	wait("")

	a := free()
	st.PanelAPI = a
	writeState(t, dir, st)
	wait(a)
	resp, err := http.Get("http://" + a + "/panel/v2/self")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %d", resp.StatusCode)
	}

	b := free()
	st.PanelAPI = b
	writeState(t, dir, st)
	wait(b)
	if _, err := net.DialTimeout("tcp", a, time.Second); err == nil {
		t.Fatal("the old address is still served")
	}

	cancel()
	<-done
	if _, err := net.DialTimeout("tcp", b, time.Second); err == nil {
		t.Fatal("served after Run returned")
	}
}

func TestLoopback(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:41990": true, "127.0.0.1:80": false, "0.0.0.0:41990": false, "[::1]:41990": false,
		"localhost:41990": false, "127.0.0.1": false, "": false, "10.0.0.1:41990": false,
	} {
		if Loopback(addr) != want {
			t.Errorf("%q: %v", addr, !want)
		}
	}
}

func TestBridge(t *testing.T) {
	for addr, want := range map[string]bool{
		"172.18.0.1:41990": true, "10.0.0.1:41990": true, "172.18.0.1:80": false, "127.0.0.1:41990": false,
		"8.8.8.8:41990": false, "0.0.0.0:41990": false, "[fd00::1]:41990": false, "": false,
	} {
		if Bridge(addr) != want {
			t.Errorf("%q: %v", addr, !want)
		}
	}
	// What is listened on: the loopback address, and the bridge only when it is sound.
	got := wanted(addons.State{PanelAPI: "127.0.0.1:41990", PanelAPIBridge: "172.18.0.1:41990"})
	if len(got) != 2 {
		t.Fatalf("wanted: %v", got)
	}
	if got := wanted(addons.State{PanelAPI: "0.0.0.0:1", PanelAPIBridge: "8.8.8.8:41990"}); len(got) != 0 {
		t.Fatalf("wanted: %v", got)
	}
}

func TestPageSize(t *testing.T) {
	for _, c := range []struct {
		n, hi int64
		want  int32
	}{{0, 100, 1}, {-5, 100, 1}, {50, 100, 50}, {101, 100, 100}, {1 << 40, 500, 500}, {5000, 1 << 40, 1000}} {
		if got := pageSize(c.n, c.hi); got != c.want {
			t.Errorf("pageSize(%d, %d) = %d, want %d", c.n, c.hi, got, c.want)
		}
	}
}
