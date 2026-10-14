package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/auth"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/presets"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/store/storetest"
)

type turnNodes struct {
	NodeRuntime
	status nodeapi.TurnStatus
	err    error
}

func (n *turnNodes) Turn(context.Context, int64) (nodeapi.TurnStatus, error) { return n.status, n.err }

// turnFixture is the API with one seeded panel and a node runtime faked for turn.
type turnFixture struct {
	t       *testing.T
	q       *db.Queries
	nodes   *turnNodes
	now     time.Time
	token   string
	csrf    string
	handler http.Handler
}

func newTurnFixture(t *testing.T) *turnFixture {
	t.Helper()
	ctx := context.Background()
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := &turnFixture{t: t, q: st.Q, now: time.Unix(1_800_000_000, 0)}
	clock := func() time.Time { return f.now }
	if err := domain.Seed(ctx, st, f.now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Q.CreateAdmin(ctx, db.CreateAdminParams{Username: "admin", PasswordHash: "x", CreatedAt: f.now.Unix()}); err != nil {
		t.Fatal(err)
	}
	sessions := auth.NewSessions(st.Q, clock, nil)
	token, sess, err := sessions.Create(ctx, 1, "127.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	f.token, f.csrf = token, sess.CsrfToken
	f.nodes = &turnNodes{}
	pool := domain.NewPool(st, clock)
	handler, _, err := New(Deps{
		Version: "test", Store: st, Sessions: sessions, Now: clock, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		IPLimit: auth.NewLimiter(10, time.Minute, time.Minute, time.Hour), UserLimit: auth.NewLimiter(10, time.Minute, time.Minute, time.Hour), TOTP: auth.NewTOTPGuard(),
		Users: domain.NewUsers(st, pool, noChanges{}, clock), Pool: pool, Changes: noChanges{}, Nodes: f.nodes,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.handler = handler
	return f
}

func (f *turnFixture) call(method, path, body string) (int, string) {
	f.t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: f.token})
	req.Header.Set("X-CSRF-Token", f.csrf)
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// anInbound is a seeded inbound of the panel's own node, by its preset id.
func (f *turnFixture) anInbound(preset string) db.Inbound {
	f.t.Helper()
	ins, err := f.q.ListInbounds(context.Background())
	if err != nil {
		f.t.Fatal(err)
	}
	for _, in := range ins {
		if in.Preset == preset {
			return in
		}
	}
	f.t.Fatalf("no seeded inbound with preset %q", preset)
	return db.Inbound{}
}

// addInbound puts another inbound on a node, bypassing the API's own checks.
func (f *turnFixture) addInbound(node int64, name, preset, port string) db.Inbound {
	f.t.Helper()
	cfg, err := presets.NewConfig(preset, "")
	if err != nil {
		f.t.Fatal(err)
	}
	in, err := f.q.CreateInbound(context.Background(), db.CreateInboundParams{NodeID: node, Name: name, Preset: preset,
		Port: port, Config: cfg, CreatedAt: f.now.Unix(), UpdatedAt: f.now.Unix()})
	if err != nil {
		f.t.Fatal(err)
	}
	return in
}

func TestTurnSaveAndView(t *testing.T) {
	f := newTurnFixture(t)
	n, err := f.q.GetNode(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.q.UpdateNode(context.Background(), db.UpdateNodeParams{Name: n.Name, Address: n.Address,
		PublicHost: "203.0.113.7", Domain: n.Domain, PublicName: n.PublicName, Enabled: n.Enabled,
		UpdatedAt: f.now.Unix(), ID: n.ID}); err != nil {
		t.Fatal(err)
	}
	mirror := f.anInbound("vless_reality_xhttp")
	body := `{"enabled":true,"inbound_id":` + strconv.FormatInt(mirror.ID, 10) +
		`,"listen_port":"56000","client_host":"127.0.0.1","client_port":"9000","streams":10,"udp":true,` +
		`"title":"","links":["https://vk.com/call/join/goodturn"],"binary_path":""}`
	code, out := f.call(http.MethodPatch, "/api/v1/nodes/1/turn", body)
	if code != http.StatusOK {
		t.Fatalf("save: %d %s", code, out)
	}
	var v TurnView
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatal(err)
	}
	if !v.Configured || !v.Enabled || v.InboundID != mirror.ID || v.Mode != "vless" ||
		v.Entry != "VLESS XHTTP WL" || v.Peer != "203.0.113.7:56000" || len(v.Links) != 1 {
		t.Fatalf("view: %+v", v)
	}
	// The row is in the store, so the node gets it on the next sync.
	row, err := f.q.GetNodeTurn(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if row.InboundID != mirror.ID || row.ListenPort != "56000" || row.ClientPort != "9000" || row.Streams != 10 {
		t.Fatalf("row: %+v", row)
	}
	// A later save keeps what it does not name.
	if code, out := f.call(http.MethodPatch, "/api/v1/nodes/1/turn", `{"enabled":false}`); code != http.StatusOK {
		t.Fatalf("off: %d %s", code, out)
	}
	if row, err := f.q.GetNodeTurn(context.Background(), 1); err != nil || row.Enabled != 0 || row.Streams != 10 {
		t.Fatalf("row after off: %+v, %v", row, err)
	}
	// A fresh node without inbound_id is refused: there is nothing to serve.
	if code, out := f.call(http.MethodPatch, "/api/v1/nodes/1/turn", `{}`); code != http.StatusOK {
		// Already configured, so a patch without fields is allowed; only creation needs it.
		t.Fatalf("patch: %d %s", code, out)
	}
	// And the setup goes away whole.
	if code, out := f.call(http.MethodDelete, "/api/v1/nodes/1/turn", ""); code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", code, out)
	}
	code, out = f.call(http.MethodGet, "/api/v1/nodes/1/turn", "")
	if code != http.StatusOK || !strings.Contains(out, `"configured":false`) {
		t.Fatalf("get after delete: %d %s", code, out)
	}
}

func TestTurnRefusesAForeignInboundAndATakenPort(t *testing.T) {
	f := newTurnFixture(t)
	other, err := f.q.CreateNode(context.Background(), db.CreateNodeParams{
		Name: "Германия", Address: "10.0.0.9:9443", PublicHost: "203.0.113.9", CreatedAt: f.now.Unix(), UpdatedAt: f.now.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	foreign := f.addInbound(other.ID, "hysteria2-other", "hysteria2", "443")
	if code, out := f.call(http.MethodPatch, "/api/v1/nodes/1/turn",
		`{"inbound_id":`+strconv.FormatInt(foreign.ID, 10)+`}`); code != http.StatusUnprocessableEntity || !strings.Contains(out, "turn_inbound") {
		t.Fatalf("another node's inbound: %d %s", code, out)
	}
	// The port of a UDP inbound of this node is not free for the server.
	f.addInbound(1, "udp-56000", "hysteria2", "56000")
	mirror := f.anInbound("vless_reality_xhttp")
	code, out := f.call(http.MethodPatch, "/api/v1/nodes/1/turn",
		`{"inbound_id":`+strconv.FormatInt(mirror.ID, 10)+`,"listen_port":"56000"}`)
	if code != http.StatusConflict || !strings.Contains(out, "port_in_use") {
		t.Fatalf("a taken port: %d %s", code, out)
	}
	// A free one goes through, and needs no inbound_id the second time.
	code, out = f.call(http.MethodPatch, "/api/v1/nodes/1/turn",
		`{"inbound_id":`+strconv.FormatInt(mirror.ID, 10)+`,"listen_port":"56001"}`)
	if code != http.StatusOK {
		t.Fatalf("a free port: %d %s", code, out)
	}
}

func TestTurnStatusOfAnOldOrSilentNode(t *testing.T) {
	f := newTurnFixture(t)
	mirror := f.anInbound("vless_reality_xhttp")
	if code, out := f.call(http.MethodPatch, "/api/v1/nodes/1/turn",
		`{"inbound_id":`+strconv.FormatInt(mirror.ID, 10)+`}`); code != http.StatusOK {
		t.Fatalf("save: %d %s", code, out)
	}
	f.nodes.err = &nodeapi.StatusError{Method: http.MethodGet, Path: "/v1/turn", Status: http.StatusNotFound}
	code, out := f.call(http.MethodGet, "/api/v1/nodes/1/turn", "")
	if code != http.StatusOK || !strings.Contains(out, `"node_too_old"`) {
		t.Fatalf("an old node: %d %s", code, out)
	}
	f.nodes.err = nil
	f.nodes.status = nodeapi.TurnStatus{Configured: true, Running: false, Restarts: 3, Error: "no_binary",
		Detail: "/usr/local/bin/vk-turn-proxy is not on the node: its image is older than this feature", CheckedAt: f.now}
	_, out = f.call(http.MethodGet, "/api/v1/nodes/1/turn", "")
	if !strings.Contains(out, `"no_binary"`) || !strings.Contains(out, "not on the node") {
		t.Fatalf("a missing binary must reach the drawer: %s", out)
	}
}
