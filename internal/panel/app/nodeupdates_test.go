package app

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/nodetls"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/nodesync"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

// updNode is a node that answers its health and takes update requests.
type updNode struct {
	mu      sync.Mutex
	version string
	update  *nodeapi.UpdateStatus
	asked   []string
}

func (n *updNode) Apply(_ context.Context, s nodeapi.DesiredState) (nodeapi.ApplyResult, error) {
	return nodeapi.ApplyResult{Revision: s.Revision}, nil
}
func (n *updNode) SetPolicies(context.Context, string, []nodeapi.Policy) error { return nil }
func (n *updNode) Counters(context.Context) (nodeapi.Counters, error) {
	return nodeapi.Counters{Epoch: "e", Idle: true}, nil
}
func (n *updNode) Ack(context.Context, string, int64) error { return nil }
func (n *updNode) Health(context.Context) (nodeapi.Health, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return nodeapi.Health{Version: n.version, Update: n.update}, nil
}
func (n *updNode) RequestUpdate(_ context.Context, version string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.asked = append(n.asked, version)
	return nil
}
func (n *updNode) set(version string, u *nodeapi.UpdateStatus) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.version, n.update = version, u
}
func (n *updNode) requests() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.asked...)
}

type nodeView struct {
	ID        int64                 `json:"id"`
	Status    string                `json:"status"`
	Version   string                `json:"version"`
	Behind    bool                  `json:"behind"`
	CanUpdate bool                  `json:"can_update"`
	Update    *nodeapi.UpdateStatus `json:"update"`
}

// A panel of 0.5.0.3 with its own node (1) and three remote ones: 2 on 0.5.0.2, 3 on
// 0.5.0.1 (before the endpoint) and 4 on 0.5.0.3. The syncers run, so the pages see health.
type updRig struct {
	t      *testing.T
	h      *harness
	fakes  map[int64]*updNode
	api    string
	csrf   map[string]string
	read   map[string]string
	full   map[string]string
	cancel context.CancelFunc
}

func newUpdRig(t *testing.T, version string) *updRig {
	t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	r := &updRig{t: t, api: "/" + adminPath + "/api/v1", cancel: stop,
		fakes: map[int64]*updNode{1: {version: version}, 2: {version: "0.5.0.2"}, 3: {version: "0.5.0.1"}, 4: {version: "0.5.0.3"}}}
	r.h = newHarness(t, func(o *Options) {
		o.Version = version
		o.Connect = func(n db.Node) (nodesync.Target, error) {
			f, ok := r.fakes[n.ID]
			if !ok {
				return nodesync.Target{}, nodesync.ErrNoNode
			}
			return nodesync.Target{Node: f, TLS: func() (*nodeapi.TLSFiles, string, error) {
				return &nodeapi.TLSFiles{CertPEM: "c", KeyPEM: "k"}, "", nil
			}, Local: n.Address == ""}, nil
		}
	})
	h := r.h
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	set := settings.New(h.st.Q)
	for k, v := range map[string]any{settings.KeyPublicHost: "203.0.113.10", settings.KeyPanelPort: 21355} {
		if err := settings.Set(ctx, set, k, v); err != nil {
			t.Fatal(err)
		}
	}
	panel, _ := nodetls.Generate("mikan-panel", x509.ExtKeyUsageClientAuth, time.Now())
	for i, host := range []string{"198.51.100.2", "198.51.100.3", "198.51.100.4"} {
		if _, _, err := domain.AddNode(ctx, h.st, panel, domain.NodeInput{Name: "N" + string(rune('A'+i)), Host: host, APIPort: 40000 + i}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	go h.p.Nodes.Run(ctx)
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	r.csrf = map[string]string{"X-CSRF-Token": h.csrf}
	key := func(scope string) map[string]string {
		resp, body := h.do(http.MethodPost, r.api+"/api-keys", map[string]any{"name": scope, "scope": scope, "password": password}, r.csrf)
		var c struct {
			Key string `json:"key"`
		}
		if resp.StatusCode != http.StatusCreated || json.Unmarshal(body, &c) != nil {
			t.Fatalf("key: %d %s", resp.StatusCode, body)
		}
		return map[string]string{"Authorization": "Bearer " + c.Key}
	}
	r.read, r.full = key("read"), key("full")
	// the syncers look at the nodes' health at once, then every few seconds
	waitFor(t, func() bool {
		for _, v := range r.nodes(nil) {
			if v.Status != "ok" {
				return false
			}
		}
		return true
	})
	return r
}

// nodes lists the nodes with the session, or with the given key (no session then).
func (r *updRig) nodes(hdr map[string]string) map[int64]nodeView {
	r.t.Helper()
	resp, body := r.h.do(http.MethodGet, r.api+"/nodes", nil, hdr)
	var all []nodeView
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &all) != nil {
		r.t.Fatalf("nodes: %d %s", resp.StatusCode, body)
	}
	out := map[int64]nodeView{}
	for _, v := range all {
		out[v.ID] = v
	}
	return out
}

func (r *updRig) ask(id int64, hdr map[string]string) (int, string) {
	resp, body := r.h.do(http.MethodPost, r.api+"/nodes/"+strconv.FormatInt(id, 10)+"/update", nil, hdr)
	return resp.StatusCode, string(body)
}

func (r *updRig) refused(id int64, hdr map[string]string, status int, code string) {
	r.t.Helper()
	if got, body := r.ask(id, hdr); got != status || !strings.Contains(body, code) {
		r.t.Errorf("node %d: %d %s, want %d %s", id, got, body, status, code)
	}
}

func (r *updRig) anonymous() { r.h.client.Jar, _ = cookiejar.New(nil) }

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("it did not happen in time")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// The Nodes page and its Update button: each node's version against the panel's, who can be
// updated from here, what is refused (the panel's own node, a node that is not behind, an
// old one, a node that is gone), and who may ask: an admin with a session, or a full key.
func TestUpdatingNodesFromThePanel(t *testing.T) {
	r := newUpdRig(t, "0.5.0.3")
	for id, want := range map[int64]nodeView{
		1: {Behind: false, CanUpdate: false}, // the panel's own node updates with the panel
		2: {Behind: true, CanUpdate: true},
		3: {Behind: true, CanUpdate: false}, // 0.5.0.1 has no endpoint: the admin runs mikan update there once
		4: {Behind: false, CanUpdate: true},
	} {
		if v := r.nodes(nil)[id]; v.Behind != want.Behind || v.CanUpdate != want.CanUpdate || v.Version != r.fakes[id].version {
			t.Errorf("node %d: %+v, want behind=%v can_update=%v", id, v, want.Behind, want.CanUpdate)
		}
	}
	r.refused(1, r.csrf, http.StatusConflict, "local_node_update")
	r.refused(3, r.csrf, http.StatusConflict, "node_cannot_update")
	r.refused(4, r.csrf, http.StatusConflict, "node_not_behind")
	r.refused(99, r.csrf, http.StatusNotFound, "not_found")
	if got, _ := r.ask(2, nil); got != http.StatusForbidden {
		t.Errorf("a session without the CSRF token: %d", got)
	}
	for id, f := range r.fakes {
		if len(f.requests()) != 0 {
			t.Fatalf("node %d was asked: %v", id, f.requests())
		}
	}

	// nobody without a login, and a key that may only read
	r.anonymous()
	if got, _ := r.ask(2, nil); got != http.StatusUnauthorized {
		t.Errorf("anonymous: %d", got)
	}
	if got, _ := r.ask(2, r.read); got != http.StatusForbidden {
		t.Errorf("a read key: %d", got)
	}
	if len(r.fakes[2].requests()) != 0 {
		t.Fatal("a read key updated a node")
	}

	// a full key may: the node is asked for the panel's own version, never another
	if got, body := r.ask(2, r.full); got != http.StatusAccepted {
		t.Fatalf("a full key: %d %s", got, body)
	}
	if got := r.fakes[2].requests(); len(got) != 1 || got[0] != "0.5.0.3" {
		t.Fatalf("node 2 was asked for %v", got)
	}
	// the request is remembered: the page says it runs, and a second one is refused meanwhile
	r.refused(2, r.full, http.StatusConflict, "update_running")
	if v := r.nodes(r.full)[2]; v.Update == nil || v.Update.State != "running" || v.Update.Version != "0.5.0.3" || v.Update.From != "0.5.0.2" {
		t.Errorf("the node's update: %+v", v.Update)
	}
	// the updater reports it went back, and why
	r.fakes[2].set("0.5.0.2", &nodeapi.UpdateStatus{State: "failed", Version: "0.5.0.3", From: "0.5.0.2", Error: "mikan 0.5.0.3 did not start: going back"})
	waitFor(t, func() bool {
		v := r.nodes(r.full)[2]
		return v.Update != nil && v.Update.State == "failed" && strings.Contains(v.Update.Error, "going back") && v.Behind && v.CanUpdate
	})
	// the audit trail has what was asked, and by which key
	var audit string
	if err := r.h.st.DB.QueryRow("SELECT details FROM audit_log WHERE action = 'node.update_version'").Scan(&audit); err != nil ||
		!strings.Contains(audit, `"version":"0.5.0.3"`) || !strings.Contains(audit, `"api_key"`) {
		t.Fatalf("audit: %q, %v", audit, err)
	}
	// the node is back as it was, and the next try goes out
	r.fakes[2].set("0.5.0.2", nil)
	if got, body := r.ask(2, r.full); got != http.StatusAccepted {
		t.Fatalf("second try: %d %s", got, body)
	}
	// it comes up on the new version: nothing is behind there any more
	r.fakes[2].set("0.5.0.3", nil)
	waitFor(t, func() bool { v := r.nodes(r.full)[2]; return !v.Behind && v.Version == "0.5.0.3" })
	r.refused(2, r.full, http.StatusConflict, "node_not_behind")

	// a node that does not answer cannot be asked
	r.fakes[2].set("0.5.0.2", nil)
	waitFor(t, func() bool { return r.nodes(r.full)[2].Behind })
}

// A panel that runs no release (a development build) updates no node.
func TestADevelopmentPanelUpdatesNoNode(t *testing.T) {
	r := newUpdRig(t, "dev")
	for id, v := range r.nodes(nil) {
		if v.Behind {
			t.Errorf("node %d is behind a panel that is no release", id)
		}
	}
	r.refused(2, r.csrf, http.StatusConflict, "panel_not_release")
	if resp, body := r.h.do(http.MethodPost, r.api+"/nodes/update-all", nil, r.csrf); resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "panel_not_release") {
		t.Errorf("update all: %d %s", resp.StatusCode, body)
	}
	for id, f := range r.fakes {
		if len(f.requests()) != 0 {
			t.Errorf("node %d was asked: %v", id, f.requests())
		}
	}
}

// "Nodes follow the panel": on by default, shown in the updates card, switched by the admin
// with a session only; and when on, the panel itself updates the nodes one at a time.
func TestNodesFollowThePanelSetting(t *testing.T) {
	r := newUpdRig(t, "0.5.0.3")
	type updates struct {
		NodesFollow bool `json:"nodes_follow"`
	}
	read := func(hdr map[string]string) updates {
		t.Helper()
		resp, body := r.h.do(http.MethodGet, r.api+"/updates", nil, hdr)
		var u updates
		if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &u) != nil || !strings.Contains(string(body), `"nodes_follow"`) {
			t.Fatalf("updates: %d %s", resp.StatusCode, body)
		}
		return u
	}
	if !read(nil).NodesFollow {
		t.Fatal("nodes do not follow the panel by default")
	}
	if resp, body := r.h.do(http.MethodPatch, r.api+"/updates", map[string]any{"nodes_follow": false}, r.csrf); resp.StatusCode != http.StatusOK || read(nil).NodesFollow {
		t.Fatalf("switch off: %d %s", resp.StatusCode, body)
	}
	if v, ok, err := settings.Get[bool](context.Background(), settings.New(r.h.st.Q), settings.KeyNodesFollow); err != nil || !ok || v {
		t.Fatalf("the setting is not stored: %v %v %v", v, ok, err)
	}
	// a key does not change it
	r.anonymous()
	if resp, _ := r.h.do(http.MethodPatch, r.api+"/updates", map[string]any{"nodes_follow": true}, r.full); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a full key: %d", resp.StatusCode)
	}
	if read(r.read).NodesFollow {
		t.Fatal("a key switched it on")
	}
	// off: the panel leaves the nodes alone, however long it looks
	if err := r.h.p.NodeUpdates.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(r.fakes[2].requests()) != 0 {
		t.Fatal("a node was updated with the switch off")
	}
	// on again, and the panel asks the node that is behind, only that one, for its own version
	if resp, _ := r.h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	csrf := map[string]string{"X-CSRF-Token": r.h.csrf}
	if resp, body := r.h.do(http.MethodPatch, r.api+"/updates", map[string]any{"nodes_follow": true}, csrf); resp.StatusCode != http.StatusOK {
		t.Fatalf("switch on: %d %s", resp.StatusCode, body)
	}
	if err := r.h.p.NodeUpdates.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[int64]int{1: 0, 2: 1, 3: 0, 4: 0} {
		if got := len(r.fakes[id].requests()); got != want {
			t.Errorf("node %d was asked %d times, want %d", id, got, want)
		}
	}
	if got := r.fakes[2].requests(); len(got) == 1 && got[0] != "0.5.0.3" {
		t.Errorf("asked for %v", got)
	}
}
