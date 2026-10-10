package app

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/nodehello"
	"mikan/internal/nodetls"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/nodesync"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

// chkNode answers its health a minute ahead of the panel's clock, with one listener whose
// port another program holds, and predates the node's own diagnosis.
type chkNode struct {
	mu   sync.Mutex
	down error
}

func (n *chkNode) Apply(_ context.Context, s nodeapi.DesiredState) (nodeapi.ApplyResult, error) {
	return nodeapi.ApplyResult{Revision: s.Revision}, nil
}
func (n *chkNode) SetPolicies(context.Context, string, []nodeapi.Policy) error { return nil }
func (n *chkNode) Counters(context.Context) (nodeapi.Counters, error) {
	return nodeapi.Counters{Epoch: "e", Idle: true}, nil
}
func (n *chkNode) Ack(context.Context, string, int64) error { return nil }
func (n *chkNode) Health(context.Context) (nodeapi.Health, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.down != nil {
		return nodeapi.Health{}, n.down
	}
	return nodeapi.Health{Version: "test", Time: time.Now().Add(time.Minute), Listeners: []nodeapi.ListenerStatus{
		{Name: "vless-reality", OK: true}, {Name: "tuic", Code: nodeapi.ListenerAddrInUse, Error: "listen udp :8443: bind: address already in use"},
	}}, nil
}
func (n *chkNode) Diagnose(context.Context) (nodeapi.Diagnosis, error) {
	return nodeapi.Diagnosis{}, &nodeapi.StatusError{Method: http.MethodPost, Path: "/v1/diagnose", Status: http.StatusNotFound}
}

type checkView struct {
	Items []struct {
		ID     string            `json:"id"`
		Status string            `json:"status"`
		Code   string            `json:"code"`
		Fix    string            `json:"fix"`
		Params map[string]string `json:"params"`
	} `json:"items"`
	Report string `json:"report"`
}

// "Check node" names what is wrong with a node and how to fix it, and its report hides the
// node's address and API port. A node that starts says hello, and the panel answers with
// how it reached the node back; the Nodes page shows that too.
func TestCheckNodeAndHello(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	panel, _ := nodetls.Generate("mikan-panel", x509.ExtKeyUsageClientAuth, time.Now())
	fake := &chkNode{}
	h := newHarness(t, func(o *Options) {
		o.PanelCert = func() (nodetls.Pair, error) { return panel, nil }
		o.Connect = func(n db.Node) (nodesync.Target, error) {
			if n.Address == "" {
				return nodesync.Target{}, nodesync.ErrNoNode
			}
			return nodesync.Target{Node: fake, TLS: func() (*nodeapi.TLSFiles, string, error) {
				return &nodeapi.TLSFiles{CertPEM: "c", KeyPEM: "k"}, "", nil
			}, Address: n.Address}, nil
		}
	})
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	set := settings.New(h.st.Q)
	for k, v := range map[string]any{settings.KeyPublicHost: "203.0.113.10", settings.KeyPanelPort: 21355} {
		if err := settings.Set(ctx, set, k, v); err != nil {
			t.Fatal(err)
		}
	}
	n, raw, err := domain.AddNode(ctx, h.st, panel, domain.NodeInput{Name: "B", Host: "127.0.0.1", APIPort: 40000, PanelURL: h.ts.URL}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	go h.p.Nodes.Run(ctx)
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	api := "/" + adminPath + "/api/v1"
	waitFor(t, func() bool { _, ok := h.p.Nodes.Syncer(n.ID); return ok })

	resp, body := h.do(http.MethodPost, api+"/nodes/"+strconv.FormatInt(n.ID, 10)+"/check", nil, csrf)
	var v checkView
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &v) != nil {
		t.Fatalf("check: %d %s", resp.StatusCode, body)
	}
	got := map[string]string{}
	for _, it := range v.Items {
		got[it.ID] = it.Status + "/" + it.Code + "/" + it.Fix
	}
	for id, want := range map[string]string{
		"link":      "ok//",
		"hello":     "skip/none/",
		"skew":      "warn/skew/sync_time",
		"listeners": "fail/some_down/",
		"listener":  "fail/busy/free_port",
		"diagnose":  "skip/old_node/update_node",
	} {
		if got[id] != want {
			t.Errorf("%s: %q, want %q (%s)", id, got[id], want, body)
		}
	}
	for _, leak := range []string{"127.0.0.1", "40000", "203.0.113.10"} {
		if strings.Contains(v.Report, leak) {
			t.Errorf("the report shows %s:\n%s", leak, v.Report)
		}
	}

	// The node says hello: the panel dials it back and answers, signed.
	key, err := nodetls.DecodeKey(raw)
	if err != nil || key.PanelURL != h.ts.URL {
		t.Fatalf("key: %q %v", key.PanelURL, err)
	}
	r := nodehello.Send(ctx, key, h.ts.Client(), time.Now())
	if !r.OK || r.SeenIP != "127.0.0.1" || r.Params[nodehello.IPDiffers] != "" {
		t.Fatalf("hello: %+v", r)
	}
	// A node that does not answer: the hello says why.
	fake.mu.Lock()
	fake.down = &nodeapi.StatusError{Method: http.MethodGet, Path: "/v1/health", Status: http.StatusBadGateway}
	fake.mu.Unlock()
	if r := nodehello.Send(ctx, key, h.ts.Client(), time.Now()); r.OK || r.Code != nodeapi.LinkHTTPStatus {
		t.Fatalf("hello of a node that fails: %+v", r)
	}
	resp, body = h.do(http.MethodGet, api+"/nodes", nil, csrf)
	var list []struct {
		ID        int64  `json:"id"`
		ErrorCode string `json:"error_code"`
		Hello     *struct {
			OK   bool   `json:"ok"`
			Code string `json:"code"`
		} `json:"hello"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &list) != nil {
		t.Fatalf("nodes: %d %s", resp.StatusCode, body)
	}
	for _, x := range list {
		if x.ID == n.ID && (x.Hello == nil || x.Hello.OK || x.Hello.Code != nodeapi.LinkHTTPStatus || x.ErrorCode != nodeapi.LinkHTTPStatus) {
			t.Fatalf("the Nodes page: %s", body)
		}
	}

	// Another key, or a stranger: the route is any unknown path.
	other, _ := nodetls.Generate("node-9.mikan", x509.ExtKeyUsageServerAuth, time.Now())
	stranger := key
	stranger.CertPEM, stranger.KeyPEM = other.CertPEM, other.KeyPEM
	if r := nodehello.Send(ctx, stranger, h.ts.Client(), time.Now()); r.Code != nodehello.PanelRejected {
		t.Fatalf("a stranger's hello: %+v", r)
	}
	resp, body = h.do(http.MethodPost, nodehello.Path, map[string]string{"cert": "x"}, nil)
	if resp.StatusCode != http.StatusNotFound || string(body) != "404 Not Found\n" {
		t.Fatalf("a bad hello: %d %q", resp.StatusCode, body)
	}
}
