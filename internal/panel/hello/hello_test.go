package hello

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"sync"
	"testing"
	"time"

	"mikan/internal/nodehello"
	"mikan/internal/nodetls"
)

type stand struct {
	panel   nodetls.Pair
	key     nodetls.Key // the node's join key
	srv     *httptest.Server
	h       *Handler
	ip      string
	host    string
	now     time.Time
	mu      sync.Mutex
	records []nodehello.Result
	dial    nodehello.Result
}

func newStand(t *testing.T) *stand {
	t.Helper()
	now := time.Now()
	panel, err := nodetls.Generate("mikan-panel", x509.ExtKeyUsageClientAuth, now)
	if err != nil {
		t.Fatal(err)
	}
	node, err := nodetls.Generate("node-2.mikan", x509.ExtKeyUsageServerAuth, now)
	if err != nil {
		t.Fatal(err)
	}
	panelPin, _ := nodetls.Fingerprint(panel.CertPEM)
	nodePin, _ := nodetls.Fingerprint(node.CertPEM)
	s := &stand{panel: panel, ip: "203.0.113.7", host: "203.0.113.7", now: now, dial: nodehello.Result{OK: true}}
	s.h = New(Deps{
		Lookup: func(_ context.Context, pin string) (Node, bool) {
			if pin == nodePin {
				return Node{ID: 2, Host: s.host}, true
			}
			return Node{}, false
		},
		Dial: func(context.Context, int64) nodehello.Result { return s.dial },
		Record: func(id int64, r nodehello.Result, _ time.Time) {
			s.mu.Lock()
			s.records = append(s.records, r)
			s.mu.Unlock()
		},
		Panel:    func() (nodetls.Pair, error) { return panel, nil },
		ClientIP: func(*http.Request) string { return s.ip },
		Resolve: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("203.0.113.9")}, nil
		},
		Now: func() time.Time { return s.now },
	})
	s.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.h.Serve(w, r) {
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.srv.Close)
	s.key = nodetls.Key{Port: 31234, PanelPin: panelPin, CertPEM: node.CertPEM, KeyPEM: node.KeyPEM, PanelURL: s.srv.URL}
	return s
}

func (s *stand) send() nodehello.Result {
	return nodehello.Send(context.Background(), s.key, s.srv.Client(), s.now)
}

// post sends a hello as it is and returns the status.
func (s *stand) post(t *testing.T, req nodehello.Request) int {
	t.Helper()
	raw, _ := json.Marshal(req)
	resp, err := s.srv.Client().Post(s.srv.URL+nodehello.Path, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestHelloAnswered(t *testing.T) {
	s := newStand(t)
	r := s.send()
	if !r.OK || r.SeenIP != "203.0.113.7" || r.Host != "203.0.113.7" || r.Params[nodehello.IPDiffers] != "" {
		t.Fatalf("result: %+v", r)
	}
	if len(s.records) != 1 || !s.records[0].OK {
		t.Fatalf("records: %+v", s.records)
	}
	// The panel could not dial the node back: its reason goes to the node.
	s.dial = nodehello.Result{Code: "timeout", Params: map[string]string{"port": "31234"}}
	if r := s.send(); r.OK || r.Code != "timeout" || r.Params["port"] != "31234" {
		t.Fatalf("a failed dial: %+v", r)
	}
}

func TestHelloFromAnotherAddress(t *testing.T) {
	s := newStand(t)
	s.ip = "198.51.100.4"
	if r := s.send(); r.Params[nodehello.IPDiffers] != "1" || r.SeenIP != "198.51.100.4" {
		t.Fatalf("an IP host: %+v", r)
	}
	// A name: what it resolves to counts.
	s.host = "node.example.com"
	if r := s.send(); r.Params[nodehello.IPDiffers] != "1" {
		t.Fatalf("a name elsewhere: %+v", r)
	}
	s.ip = "203.0.113.9"
	if r := s.send(); r.Params[nodehello.IPDiffers] != "" {
		t.Fatalf("a name that leads here: %+v", r)
	}
}

func TestHelloRefused(t *testing.T) {
	s := newStand(t)
	valid := func() nodehello.Request {
		req, err := nodehello.NewRequest(s.key, s.now)
		if err != nil {
			t.Fatal(err)
		}
		return req
	}
	// Signed with another key than the certificate's.
	other, _ := nodetls.Generate("node-3.mikan", x509.ExtKeyUsageServerAuth, s.now)
	forged := valid()
	sig, _ := nodetls.Sign(other.KeyPEM, []byte("mikan-hello/1\n"+strconv.FormatInt(forged.Time, 10)+"\n"+forged.Nonce))
	forged.Sig = base64.StdEncoding.EncodeToString(sig)
	// Out of the time window either way.
	expired, err := nodehello.NewRequest(s.key, s.now.Add(-6*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	future, _ := nodehello.NewRequest(s.key, s.now.Add(6*time.Minute))
	// A node the panel does not have: its own key, its own certificate.
	stranger := s.key
	stranger.CertPEM, stranger.KeyPEM = other.CertPEM, other.KeyPEM
	unknown, _ := nodehello.NewRequest(stranger, s.now)
	tampered := valid()
	tampered.Time++
	for name, req := range map[string]nodehello.Request{"bad signature": forged, "expired": expired, "future": future, "unknown node": unknown, "tampered time": tampered, "empty": {}} {
		if code := s.post(t, req); code != http.StatusNotFound {
			t.Errorf("%s: %d", name, code)
		}
	}
	// A replayed hello is refused the second time.
	req := valid()
	if code := s.post(t, req); code != http.StatusOK {
		t.Fatalf("first: %d", code)
	}
	if code := s.post(t, req); code != http.StatusNotFound {
		t.Fatalf("replayed: %d", code)
	}
	// Other methods are not the hello at all.
	resp, err := s.srv.Client().Get(s.srv.URL + nodehello.Path)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET: %d", resp.StatusCode)
	}
	if len(s.records) != 1 {
		t.Fatalf("records of refused hellos: %+v", s.records)
	}
	// The node learns the panel does not know it.
	if r := nodehello.Send(context.Background(), stranger, s.srv.Client(), s.now); r.Code != nodehello.PanelRejected {
		t.Fatalf("unknown node: %+v", r)
	}
}

// An address gets so many tries, valid or not.
func TestHelloRateLimited(t *testing.T) {
	s := newStand(t)
	for i := range perIP {
		if r := s.send(); !r.OK {
			t.Fatalf("hello %d: %+v", i, r)
		}
	}
	if r := s.send(); r.Code != nodehello.PanelRejected {
		t.Fatalf("over the limit: %+v", r)
	}
	s.ip = "198.51.100.4"
	if r := s.send(); !r.OK {
		t.Fatalf("another address: %+v", r)
	}
	s.now = s.now.Add(perWindow + time.Second)
	s.ip = "203.0.113.7"
	if r := s.send(); !r.OK {
		t.Fatalf("after the window: %+v", r)
	}
}

// The node believes only answers signed by the panel its key pins.
func TestHelloAnswerMustBeThePanels(t *testing.T) {
	s := newStand(t)
	other, _ := nodetls.Generate("mikan-panel", x509.ExtKeyUsageClientAuth, s.now)
	s.key.PanelPin, _ = nodetls.Fingerprint(other.CertPEM)
	if r := s.send(); r.Code != nodehello.PanelUnverified {
		t.Fatalf("another panel's answer: %+v", r)
	}
	// A key of an older panel names no address.
	s.key.PanelURL = ""
	if r := s.send(); r.Code != nodehello.NoPanelURL {
		t.Fatalf("no address: %+v", r)
	}
	// A panel that cannot be reached.
	s.key.PanelURL = "https://127.0.0.1:1"
	if r := s.send(); r.Code != nodehello.PanelUnreachable {
		t.Fatalf("unreachable: %+v", r)
	}
}
