package nodesync

import (
	"context"
	"errors"
	"testing"

	"mikan/internal/nodeapi"
)

// refusingNode is a node that does not take a state while down is set.
type refusingNode struct {
	fakeNode
	down bool
}

func (r *refusingNode) Apply(ctx context.Context, s nodeapi.DesiredState) (nodeapi.ApplyResult, error) {
	if r.down {
		return nodeapi.ApplyResult{}, errors.New("node down")
	}
	return r.fakeNode.Apply(ctx, s)
}

// Links drop the pin only once the node serves the public certificate: until it took the
// state with it, it serves the self-signed one, and an unpinned link would fail there.
func TestLinksFollowTheCertificateTheNodeServes(t *testing.T) {
	s, _, _, _, _ := setup(t)
	node := &refusingNode{}
	cert, pin := "self-signed", "abc123"
	s.node = node
	s.tls = func() (*nodeapi.TLSFiles, string, error) {
		return &nodeapi.TLSFiles{CertPEM: cert, KeyPEM: "k"}, pin, nil
	}
	ctx := context.Background()
	if _, ok := s.m.ServedPin(s.id); ok {
		t.Fatal("a pin known before the node took anything")
	}
	s.applyState(ctx)
	if got, ok := s.m.ServedPin(s.id); !ok || got != "abc123" {
		t.Fatalf("after the first state: %q %v", got, ok)
	}
	gen := s.m.Generation()

	// The node gets a public certificate, but is down: links keep the pin.
	cert, pin = "public", ""
	node.down = true
	s.applyState(ctx)
	if got, _ := s.m.ServedPin(s.id); got != "abc123" || s.m.Generation() != gen {
		t.Fatalf("a state the node did not take moved the links: %q", got)
	}
	// It takes it: the pin goes, and the subscriptions are built again.
	node.down = false
	s.retry = retry{}
	s.mu.Lock()
	s.failedKey = ""
	s.mu.Unlock()
	s.applyState(ctx)
	if got, ok := s.m.ServedPin(s.id); !ok || got != "" || s.m.Generation() == gen {
		t.Fatalf("after the public certificate: %q %v, generation %d → %d", got, ok, gen, s.m.Generation())
	}
}
