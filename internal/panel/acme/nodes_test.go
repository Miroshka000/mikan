package acme

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/storetest"
	"mikan/internal/panel/tlscert"
)

// fakeNode is a node's challenge endpoint: what the panel put there, or the error it answers.
type fakeNode struct {
	mu      sync.Mutex
	tokens  map[string]string
	err     error // what PUT answers
	old     bool  // a node before the endpoint: 404 to everything
	cleaned int
}

var notFound = &nodeapi.StatusError{Method: http.MethodPut, Path: "/v1/acme/challenge/x", Status: http.StatusNotFound}

func (n *fakeNode) PresentChallenge(_ context.Context, token, keyAuth string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.old {
		return notFound
	}
	if n.err != nil {
		return n.err
	}
	if n.tokens == nil {
		n.tokens = map[string]string{}
	}
	n.tokens[token] = keyAuth
	return nil
}

func (n *fakeNode) CleanUpChallenge(_ context.Context, token string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.old {
		return notFound
	}
	delete(n.tokens, token)
	n.cleaned++
	return nil
}

// answers says whether the node answers token with a key authorization for it.
func (n *fakeNode) answers(token string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	ka, ok := n.tokens[token]
	return ok && strings.HasPrefix(ka, token+".")
}

func testManager(t *testing.T, domain string) (*Manager, *settings.Settings) {
	t.Helper()
	ctx := context.Background()
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	set := settings.New(st.Q)
	if err := settings.Set(ctx, set, settings.KeyDomain, domain); err != nil {
		t.Fatal(err)
	}
	fallback, err := tlscert.LoadOrCreateSelfSigned(t.TempDir(), domain, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	holder := &tlscert.Holder{}
	holder.Set(fallback)
	return New(t.TempDir(), holder, fallback, set, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Now), set
}

func nodesFor(m *Manager, targets ...NodeTarget) *Nodes {
	return m.Nodes(func(context.Context) ([]NodeTarget, error) { return targets, nil })
}

// The panel orders the node's certificate and the node answers the CA: the token goes to
// the node, the certificate stays with the panel, the node hears of it with its state.
func TestNodeCertificateIsOrderedThroughTheNode(t *testing.T) {
	node := &fakeNode{}
	ca := newFakeCA(t, func(_, token, _ string) bool { return node.answers(token) })
	m, _ := testManager(t, "panel.example.com")
	nodes := nodesFor(m, NodeTarget{ID: 7, Host: "nl.example.com", Client: node})
	var changed []int64
	nodes.OnChange(func(id int64) { changed = append(changed, id) })

	nodes.due(context.Background())
	if st, _ := nodes.Status(7); st.Error != "" {
		t.Fatalf("order failed: %+v", st)
	}
	cert, issuedBy := nodes.Cert(7, "nl.example.com")
	if cert == nil || issuedBy != CALetsEncrypt || !tlscert.Covers(cert.Leaf, "nl.example.com") {
		t.Fatalf("no certificate for the node: %v %q", cert, issuedBy)
	}
	if st, ok := nodes.Status(7); !ok || st.Error != "" || st.CA != CALetsEncrypt || st.NotAfter == nil {
		t.Fatalf("status: %+v", st)
	}
	if len(changed) != 1 || changed[0] != 7 {
		t.Fatalf("the node was not told: %v", changed)
	}
	// The probe before the order, and the CA's token after it.
	if node.cleaned != 2 || len(node.tokens) != 0 {
		t.Fatalf("the token stayed on the node: %d cleaned, %v", node.cleaned, node.tokens)
	}
	// A valid certificate is not ordered again, not even when woken.
	nodes.Wake()
	clear(nodes.next)
	nodes.due(context.Background())
	if ca.orders != 1 {
		t.Fatalf("%d orders", ca.orders)
	}
	if c, _ := nodes.Cert(7, "other.example.com"); c != nil {
		t.Fatal("a certificate for another host")
	}
}

// A node older than the challenge endpoint answers 404: it keeps its pinned self-signed
// certificate, and the admin is told to update it.
func TestOldNodeKeepsItsSelfSignedCertificate(t *testing.T) {
	node := &fakeNode{old: true}
	ca := newFakeCA(t, func(_, token, _ string) bool { return node.answers(token) })
	m, _ := testManager(t, "panel.example.com")
	nodes := nodesFor(m, NodeTarget{ID: 3, Host: "old.example.com", Client: node})
	nodes.due(context.Background())
	if st, _ := nodes.Status(3); st.Error != CodeNodeOutdated {
		t.Fatalf("status: %+v", st)
	}
	if ca.orders != 0 {
		t.Fatalf("an old node cost %d orders at the CA", ca.orders)
	}
	// Asked again within a minute: the admin updates it next, and the card must not keep
	// saying "too old" once it is new.
	if wait := nodes.next[3].Sub(time.Now()); wait > outdatedRetry {
		t.Fatalf("next look in %s", wait)
	}
	if c, _ := nodes.Cert(3, "old.example.com"); c != nil {
		t.Fatal("a certificate without an order")
	}
	// Updated: the next look orders, the old answer is gone.
	node.mu.Lock()
	node.old = false
	node.mu.Unlock()
	nodes.mu.Lock()
	nodes.next[3] = time.Time{}
	nodes.mu.Unlock()
	nodes.due(context.Background())
	if st, _ := nodes.Status(3); st.Error != "" || ca.orders != 1 {
		t.Fatalf("after the update: %+v, %d orders", st, ca.orders)
	}
	// The attempt is in the node's log.
	if st, _ := nodes.Status(3); len(st.Attempts) != 1 || st.Attempts[0].Error != "" {
		t.Fatalf("attempts: %+v", st.Attempts)
	}
}

// Port 80 of the node is held by a web server: the status names it.
func TestNodePort80Busy(t *testing.T) {
	node := &fakeNode{err: &nodeapi.Error{Code: nodeapi.CodePort80Busy, Message: "nginx"}}
	newFakeCA(t, func(_, token, _ string) bool { return node.answers(token) })
	m, _ := testManager(t, "panel.example.com")
	nodes := nodesFor(m, NodeTarget{ID: 4, Host: "busy.example.com", Client: node})
	nodes.due(context.Background())
	if st, _ := nodes.Status(4); st.Error != CodePort80Busy || st.Holder != "nginx" {
		t.Fatalf("status: %+v", st)
	}
	// The failed order is logged with who held the port.
	if st, _ := nodes.Status(4); len(st.Attempts) != 1 || st.Attempts[0].Error != CodePort80Busy || st.Attempts[0].Holder != "nginx" {
		t.Fatalf("attempts: %+v", st.Attempts)
	}
}

// Nothing is ordered for a node with a certificate of its own or a private address; Renew
// refuses them.
func TestNodesWithoutOrders(t *testing.T) {
	node := &fakeNode{}
	ca := newFakeCA(t, func(_, token, _ string) bool { return node.answers(token) })
	m, _ := testManager(t, "panel.example.com")
	nodes := nodesFor(m, NodeTarget{ID: 5, Host: "own.example.com", HasOwn: true, Client: node}, NodeTarget{ID: 6, Host: "10.0.0.5", Client: node})
	nodes.due(context.Background())
	if ca.orders != 0 {
		t.Fatalf("%d orders", ca.orders)
	}
	if st, _ := nodes.Status(6); st.Error != "no_public_host" {
		t.Fatalf("private: %+v", st)
	}
	if _, _, err := nodes.Renew(context.Background(), 5, time.Second); err != ErrNoTarget {
		t.Fatalf("renew of a node with its own certificate: %v", err)
	}
}

// "Get now" waits for the order and returns its outcome.
func TestNodeRenewWaits(t *testing.T) {
	node := &fakeNode{}
	newFakeCA(t, func(_, token, _ string) bool { return node.answers(token) })
	m, _ := testManager(t, "panel.example.com")
	nodes := nodesFor(m, NodeTarget{ID: 8, Host: "de.example.com", Client: node})
	st, done, err := nodes.Renew(context.Background(), 8, 30*time.Second)
	if err != nil || !done || st.Error != "" || st.CA != CALetsEncrypt {
		t.Fatalf("renew: %+v %v %v", st, done, err)
	}
}

// The panel answers its own challenge on its port (MIKAN_ACME_LISTEN), and "Request now"
// returns the certificate it got.
func TestPanelOrderOnItsOwnPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	t.Setenv("MIKAN_ACME_LISTEN", addr)
	newFakeCA(t, func(_, token, _ string) bool {
		resp, err := http.Get("http://" + addr + "/.well-known/acme-challenge/" + token)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode == 200 && strings.HasPrefix(string(body), token+".")
	})
	m, _ := testManager(t, "vpn.example.com")
	st, done := m.RenewNow(context.Background(), 30*time.Second)
	if !done || st.Kind != "acme" || st.CA != CALetsEncrypt || st.Error != "" || m.Public() == nil {
		t.Fatalf("renew now: %+v %v", st, done)
	}
	if m.challenge.Listening() {
		t.Fatal("port 80 is still held after the order")
	}
}
