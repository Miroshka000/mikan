package nodesync

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"testing"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/nodehello"
	"mikan/internal/nodetls"
	"mikan/internal/panel/domain"
)

// sick is a node whose health check fails while err is set.
type sick struct {
	*fakeNode
	err error
}

func (s *sick) Health(ctx context.Context) (nodeapi.Health, error) {
	if s.err != nil {
		return nodeapi.Health{}, s.err
	}
	return s.fakeNode.Health(ctx)
}

// A node that does not answer says why, since when, and when it last did; a node that does
// says how far its clock is from the panel's.
func TestHealthNamesTheFailureAndTheClock(t *testing.T) {
	s, fake, _, _, now := setup(t)
	ctx := context.Background()
	s.address = "203.0.113.5:31234"
	n := &sick{fakeNode: fake}
	s.node = n

	fake.health = nodeapi.Health{Version: "1"}
	s.refreshHealth(ctx)
	hv := s.Health()
	if !hv.OK || hv.Skew != nil || !hv.LastOK.Equal(*now) {
		t.Fatalf("an old node, no clock: %+v", hv)
	}
	fake.health.Time = now.Add(45 * time.Second)
	s.refreshHealth(ctx)
	if hv := s.Health(); hv.Skew == nil || *hv.Skew != 45*time.Second {
		t.Fatalf("a node 45 s ahead: %+v", hv.Skew)
	}
	okAt := *now

	*now = now.Add(time.Minute)
	refused := fmt.Errorf("%w: %w", nodeapi.ErrUnavailable, &net.OpError{Op: "dial", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}})
	n.err = refused
	s.refreshHealth(ctx)
	hv = s.Health()
	if hv.OK || hv.Code != nodeapi.LinkRefused || hv.Params["port"] != "31234" || !hv.Since.Equal(*now) || !hv.LastOK.Equal(okAt) || hv.Skew != nil {
		t.Fatalf("refused: %+v", hv)
	}
	since := *now
	*now = now.Add(time.Minute)
	s.refreshHealth(ctx)
	if hv := s.Health(); !hv.Since.Equal(since) {
		t.Fatalf("the same failure goes on since %v, got %v", since, hv.Since)
	}
	// Another kind of failure starts anew.
	n.err = fmt.Errorf("%w: %w", nodeapi.ErrUnavailable, context.DeadlineExceeded)
	s.refreshHealth(ctx)
	if hv := s.Health(); hv.Code != nodeapi.LinkTimeout || !hv.Since.Equal(*now) {
		t.Fatalf("timeout: %+v", hv)
	}
	n.err = nil
	s.refreshHealth(ctx)
	if hv := s.Health(); !hv.OK || hv.Code != "" || !hv.Since.IsZero() || !hv.LastOK.Equal(*now) {
		t.Fatalf("back: %+v", hv)
	}
}

// resetter is a node that closes connections; err stands for an old node's 404.
type resetter struct {
	*fakeNode
	asked  [][]string
	closed int
	err    error
}

func (r *resetter) ResetSlots(_ context.Context, slots []string) (nodeapi.ResetResult, error) {
	r.asked = append(r.asked, slots)
	if r.err != nil {
		return nodeapi.ResetResult{}, r.err
	}
	return nodeapi.ResetResult{Closed: r.closed}, nil
}

// A reset goes to every node or to the one named: an old node's 404 and a node that cannot
// be asked come back next to the others' counts.
func TestResetSlotsAsksTheNodes(t *testing.T) {
	s, fake, st, _, _ := setup(t)
	ctx := context.Background()
	local := &resetter{fakeNode: fake, closed: 4}
	s.node = local
	panel, _ := nodetls.Generate("mikan-panel", x509.ExtKeyUsageClientAuth, time.Now())
	var ids []int64
	for i, host := range []string{"198.51.100.20", "198.51.100.21"} {
		n, _, err := domain.AddNode(ctx, st, panel, domain.NodeInput{Name: fmt.Sprint("n", i), Host: host, APIPort: 40000}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, n.ID)
	}
	tooOld := &nodeapi.StatusError{Method: "POST", Path: "/v1/slots/reset", Status: 404}
	old := &resetter{fakeNode: &fakeNode{}, err: tooOld}
	s.m.attach(t, ids[0], Target{Node: old, TLS: fakeTLS})
	s.m.attach(t, ids[1], Target{Node: &fakeNode{}, TLS: fakeTLS}) // no such call at all

	got := s.m.ResetSlots(ctx, 0, []string{"s1", "s2"})
	if len(got) != 3 || got[0] != (ConnReset{Node: LocalNode, Closed: 4}) ||
		got[1].Node != ids[0] || got[1].Err != tooOld || got[2].Node != ids[1] || !errors.Is(got[2].Err, nodeapi.ErrUnavailable) {
		t.Fatalf("every node: %+v", got)
	}
	if len(local.asked) != 1 || len(old.asked) != 1 || local.asked[0][1] != "s2" {
		t.Fatalf("asked %v and %v", local.asked, old.asked)
	}
	if got := s.m.ResetSlots(ctx, LocalNode, []string{"s1"}); len(got) != 1 || got[0] != (ConnReset{Node: LocalNode, Closed: 4}) || len(old.asked) != 1 {
		t.Fatalf("one node: %+v", got)
	}
	if got := s.m.ResetSlots(ctx, 99, []string{"s1"}); len(got) != 1 || !errors.Is(got[0].Err, nodeapi.ErrUnavailable) {
		t.Fatalf("a node without a syncer: %+v", got)
	}
}

func TestCheckNowAndHellos(t *testing.T) {
	s, fake, _, _, now := setup(t)
	fake.health = nodeapi.Health{Version: "2"}
	hv, ok := s.m.CheckNow(context.Background(), LocalNode)
	if !ok || !hv.OK || hv.Health.Version != "2" {
		t.Fatalf("check now: %+v %v", hv, ok)
	}
	if _, ok := s.m.CheckNow(context.Background(), 99); ok {
		t.Fatal("a node that is not there")
	}
	if _, ok := s.m.Hello(LocalNode); ok {
		t.Fatal("no hello yet")
	}
	s.m.RecordHello(LocalNode, nodehello.Result{OK: true, SeenIP: "203.0.113.7"}, *now)
	if h, ok := s.m.Hello(LocalNode); !ok || !h.Result.OK || !h.At.Equal(*now) {
		t.Fatalf("hello: %+v", h)
	}
}
