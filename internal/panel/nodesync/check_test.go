package nodesync

import (
	"context"
	"fmt"
	"net"
	"os"
	"syscall"
	"testing"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/nodehello"
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
