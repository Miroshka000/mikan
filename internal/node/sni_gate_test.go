package node

import (
	"errors"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"testing"
	"time"
)

func TestHelloGate(t *testing.T) {
	var g helloGate
	a, b := netip.MustParseAddr("203.0.113.7"), netip.MustParseAddr("203.0.113.8")
	for i := range helloPerSrc {
		if !g.enter(a) {
			t.Fatalf("place %d of %d refused", i+1, helloPerSrc)
		}
	}
	if g.enter(a) {
		t.Fatal("one source took more than its share")
	}
	if !g.enter(b) {
		t.Fatal("another source is held up by the first")
	}
	g.leave(a)
	if !g.enter(a) {
		t.Fatal("a place given back is not free")
	}
	for range helloPerSrc {
		g.leave(a)
	}
	g.leave(b)
	if g.total != 0 || len(g.by) != 0 {
		t.Fatalf("left behind: total %d, sources %d", g.total, len(g.by))
	}
	g.total = helloSlots
	if g.enter(b) {
		t.Fatal("past the total")
	}
}

// An IPv6 subscriber holds a /64: every address of it counts as one source.
func TestGateKey(t *testing.T) {
	if got := gateKey(netip.MustParseAddr("::ffff:203.0.113.7")); got != netip.MustParseAddr("203.0.113.7") {
		t.Fatalf("a mapped IPv4 address: %s", got)
	}
	x, y := gateKey(netip.MustParseAddr("2001:db8:1:2::5")), gateKey(netip.MustParseAddr("2001:db8:1:2:ffff::9"))
	if x != y || x == gateKey(netip.MustParseAddr("2001:db8:1:3::5")) {
		t.Fatalf("the /64: %s %s", x, y)
	}
}

// Connections that say nothing fill one source's share; the next is closed at once and
// not held for helloWait, and a place that frees is taken again.
func TestRouterTurnsAwayAFloodOfSilentConnections(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := &sniRouter{ln: ln, log: slog.New(slog.DiscardHandler)}
	go r.serve(nil)
	defer r.close()
	dial := func() net.Conn {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
		return c
	}
	silent := make([]net.Conn, helloPerSrc)
	for i := range silent {
		silent[i] = dial()
	}
	waiting := func() int {
		r.gate.mu.Lock()
		defer r.gate.mu.Unlock()
		return r.gate.total
	}
	for deadline := time.Now().Add(3 * time.Second); waiting() < helloPerSrc; {
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d connections are waiting", waiting(), helloPerSrc)
		}
		time.Sleep(10 * time.Millisecond)
	}
	closed := func(c net.Conn, within time.Duration) bool {
		_ = c.SetReadDeadline(time.Now().Add(within))
		_, err := c.Read(make([]byte, 1))
		return errors.Is(err, io.EOF) || err != nil && !errors.Is(err, os.ErrDeadlineExceeded)
	}
	if !closed(dial(), 2*time.Second) {
		t.Fatal("the connection past the share was held instead of closed")
	}
	silent[0].Close()
	for deadline := time.Now().Add(3 * time.Second); waiting() >= helloPerSrc; {
		if time.Now().After(deadline) {
			t.Fatal("the closed connection kept its place")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if closed(dial(), 300*time.Millisecond) {
		t.Fatal("a freed place was not given to the next connection")
	}
}
