package autotune

import (
	"context"
	"crypto/x509"
	"sync"
	"testing"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/nodetls"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/presets"
)

// slowNodes answers target checks slowly, and notes how many were open at once and what
// was asked of each node.
type slowNodes struct {
	*fakeNodes
	delay time.Duration

	mu     sync.Mutex
	active int
	most   int
	asked  map[int64][]string
}

func (s *slowNodes) CheckTarget(ctx context.Context, id int64, req nodeapi.TargetCheckRequest) (nodeapi.TargetResult, error) {
	s.mu.Lock()
	s.active++
	s.most = max(s.most, s.active)
	if s.asked == nil {
		s.asked = map[int64][]string{}
	}
	s.asked[id] = append(s.asked[id], req.Dest+"/"+req.SNI)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.active--
		s.mu.Unlock()
	}()
	select {
	case <-time.After(s.delay):
		return good(req.Dest, req.SNI), nil
	case <-ctx.Done():
		return nodeapi.TargetResult{}, ctx.Err()
	}
}

func twoNodes(t *testing.T, delay time.Duration) (*env, *slowNodes) {
	t.Helper()
	e := setup(t)
	panel, err := nodetls.Generate("mikan-panel", x509.ExtKeyUsageClientAuth, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := domain.AddNode(e.ctx, e.st, panel, domain.NodeInput{Name: "us", Host: "198.51.100.20", APIPort: 40000}, time.Now()); err != nil {
		t.Fatal(err)
	}
	slow := &slowNodes{fakeNodes: e.nodes, delay: delay}
	e.tn.nodes = slow
	return e, slow
}

// The nodes are asked side by side, so a node that does not answer holds up only its own
// checks, and a site that two inbounds of one node share is asked about once.
func TestTargetChecksRunPerNodeAndOncePerSite(t *testing.T) {
	e, slow := twoNodes(t, 150*time.Millisecond)
	start := time.Now()
	e.step(t, 0)
	took := time.Since(start)
	slow.mu.Lock()
	defer slow.mu.Unlock()
	if slow.most != 2 {
		t.Fatalf("%d checks open at once, want one per node (2)", slow.most)
	}
	for id, asked := range slow.asked {
		seen := map[string]bool{}
		for _, a := range asked {
			if seen[a] {
				t.Errorf("node %d: %s asked twice in one round", id, a)
			}
			seen[a] = true
		}
	}
	// Each node has two REALITY inbounds on the default site: one check each, answered for both.
	if len(slow.asked) != 2 {
		t.Fatalf("asked: %v", slow.asked)
	}
	for id, asked := range slow.asked {
		if len(asked) != 1 {
			t.Fatalf("node %d was asked %v", id, asked)
		}
	}
	if took > 600*time.Millisecond {
		t.Fatalf("the round took %s: the nodes were asked one after another", took)
	}
	for _, name := range []string{"vless-xhttp", "vless-vision"} {
		if s, ok := e.tn.Status(e.inbound(t, name).ID); !ok || s.TargetAt.IsZero() || !s.TargetOK {
			t.Fatalf("%s: the shared check is shown for it too: %+v", name, s)
		}
	}
}

// A shared site that failed is a failed check for each inbound that uses it, as before.
func TestSharedDeadTargetCountsForEveryInbound(t *testing.T) {
	e := setup(t)
	dead := presets.DefaultDest
	e.nodes.targets[dead] = nodeapi.TargetResult{Dest: dead, Error: "timeout"}
	e.step(t, 0)
	for _, name := range []string{"vless-xhttp", "vless-vision"} {
		e.tn.mu.Lock()
		fails := e.tn.state[e.inbound(t, name).ID].fails
		e.tn.mu.Unlock()
		if fails != 1 {
			t.Fatalf("%s: %d failed checks after one round", name, fails)
		}
	}
	if checks := e.nodes.checks.Load(); checks != 1 {
		t.Fatalf("the shared site was checked %d times", checks)
	}
}

// A round has a deadline: nodes that hold every check for the full 15 seconds cannot make
// it run on for minutes past its tick.
func TestRoundEndsAtItsDeadline(t *testing.T) {
	e, _ := twoNodes(t, time.Hour)
	e.tn.round = 200 * time.Millisecond
	start := time.Now()
	e.step(t, 0)
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("the round ran for %s", took)
	}
	if e.tn.roundTimeout() != 200*time.Millisecond {
		t.Fatal("the override")
	}
	e.tn.round = 0
	if got := e.tn.roundTimeout(); got < 2*time.Minute {
		t.Fatalf("the production deadline is %s: a scaled-down test stack would cut its own calls", got)
	}
}

// Rounds are serialized: a call for one while another runs waits, it does not race it.
func TestRoundsDoNotOverlap(t *testing.T) {
	e, slow := twoNodes(t, 30*time.Millisecond)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() { e.tn.Step(e.ctx) })
	}
	wg.Wait()
	slow.mu.Lock()
	defer slow.mu.Unlock()
	if slow.most > 2 {
		t.Fatalf("%d checks open at once: two rounds ran together", slow.most)
	}
}
