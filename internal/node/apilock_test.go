package node

import (
	"context"
	"encoding/json"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"mikan/internal/hostchan"
	"mikan/internal/nodeapi"
)

type lockFixture struct {
	t   *testing.T
	dir string
	now time.Time
	l   *apiLock
}

func newLockFixture(t *testing.T) *lockFixture {
	f := &lockFixture{t: t, dir: t.TempDir(), now: time.Unix(1_800_000_000, 0)}
	f.l = f.open()
	return f
}

// open makes the lock again over the same directory, as a restarted node does.
func (f *lockFixture) open() *apiLock {
	return newAPILock(hostchan.New(f.dir), quiet(), func() time.Time { return f.now })
}

func (f *lockFixture) admits(addr string) bool {
	return f.l.admit(netip.MustParseAddr(addr))
}

// asked is the rule the host was last asked for: nil for "off".
func (f *lockFixture) asked() []string {
	f.t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.dir, "apilock", "request"))
	if err != nil {
		f.t.Fatal(err)
	}
	var req struct {
		Allow []string `json:"allow"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		f.t.Fatal(err)
	}
	return req.Allow
}

// The lock refuses strangers and lets in the panel's networks, the addresses the panel
// came from (it may sit behind NAT) and loopback; off, it lets in anyone.
func TestAPILockAdmitsOnlyThePanel(t *testing.T) {
	f := newLockFixture(t)
	if !f.admits("192.0.2.1") {
		t.Fatal("off, the lock refused a connection")
	}
	f.l.contact("203.0.113.5:50000")
	f.l.set([]string{"198.51.100.0/24"})
	for addr, want := range map[string]bool{
		"198.51.100.9": true, "203.0.113.5": true, "::ffff:203.0.113.5": true, "127.0.0.1": true, "::1": true,
		"192.0.2.1": false, "203.0.113.6": false, "2001:db8::1": false,
	} {
		if got := f.admits(addr); got != want {
			t.Errorf("%s: admitted %v, want %v", addr, got, want)
		}
	}
	if got := f.asked(); !slices.Equal(got, []string{"198.51.100.0/24", "203.0.113.5/32"}) {
		t.Fatalf("the host was asked for %v", got)
	}
	// The panel's second address joins the rule as soon as the panel comes from it.
	f.l.contact("[2001:db8::7]:443")
	if !f.admits("2001:db8::7") {
		t.Fatal("the panel's IPv6 address is refused")
	}
	if got := f.asked(); !slices.Contains(got, "2001:db8::7/128") {
		t.Fatalf("the host was asked for %v", got)
	}
	// Off again: anyone, and the host takes the rule away.
	f.l.set(nil)
	if !f.admits("192.0.2.1") || f.asked() != nil {
		t.Fatalf("off: admitted %v, asked %v", f.admits("192.0.2.1"), f.asked())
	}
}

// After nodeapi.APILockStale without the panel the lock lifts itself in the listener and on
// the host; the panel that comes back from a new address gets in, and the rule comes back
// with that address.
func TestAPILockLiftsWithoutThePanel(t *testing.T) {
	f := newLockFixture(t)
	f.l.contact("203.0.113.5:50000")
	f.l.set([]string{})
	if f.admits("192.0.2.50") {
		t.Fatal("a stranger got in")
	}
	f.now = f.now.Add(nodeapi.APILockStale - time.Second)
	f.l.tick()
	if f.admits("192.0.2.50") || f.asked() == nil {
		t.Fatal("lifted before its time")
	}
	f.now = f.now.Add(time.Second)
	if !f.admits("192.0.2.50") {
		t.Fatal("15 minutes without the panel, and the listener still refuses")
	}
	f.l.tick()
	if f.asked() != nil {
		t.Fatalf("15 minutes without the panel, and the host keeps the rule %v", f.asked())
	}
	// The panel moved: it comes from its new address.
	f.l.contact("192.0.2.50:41000")
	if !f.admits("192.0.2.50") || f.admits("192.0.2.99") {
		t.Fatal("the lock did not come back with the panel's new address")
	}
	if got := f.asked(); !slices.Contains(got, "192.0.2.50/32") {
		t.Fatalf("the host was asked for %v", got)
	}
}

// `mikan api-lockdown off` on the server lifts the listener's lock too, until the panel
// comes again.
func TestAPILockLiftedByTheHost(t *testing.T) {
	f := newLockFixture(t)
	f.l.contact("203.0.113.5:50000")
	f.l.set([]string{})
	if err := os.WriteFile(filepath.Join(f.dir, "apilock", "lifted"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	f.l.tick()
	if !f.admits("192.0.2.50") {
		t.Fatal("lifted on the server, and the listener still refuses")
	}
	if _, err := os.Stat(filepath.Join(f.dir, "apilock", "lifted")); !os.IsNotExist(err) {
		t.Fatal("the mark stays and lifts the lock again and again")
	}
	if s, ok := hostchan.New(f.dir).ReadAPILockSeen(); !ok || s.At != 0 {
		t.Fatalf("a restarted node would lock again: %+v", s)
	}
	f.l.contact("203.0.113.5:50001")
	if f.admits("192.0.2.50") {
		t.Fatal("the panel came back and the lock did not")
	}
}

// A restarted node remembers where the panel came from: it is not locked out of a node whose
// state does not name its address.
func TestAPILockRemembersThePanelOverARestart(t *testing.T) {
	f := newLockFixture(t)
	f.l.contact("203.0.113.5:50000")
	f.l.set([]string{})
	f.now = f.now.Add(time.Minute)
	f.l = f.open()
	f.l.set([]string{})
	if !f.admits("203.0.113.5") || f.admits("192.0.2.1") {
		t.Fatal("the restarted node forgot the panel")
	}
}

// Health says when the host did not put the rule in.
func TestAPILockStatus(t *testing.T) {
	f := newLockFixture(t)
	if f.l.status() != nil {
		t.Fatal("off, and a status")
	}
	f.l.contact("203.0.113.5:50000")
	f.l.set([]string{})
	if s := f.l.status(); s == nil || !s.OK {
		t.Fatalf("just asked: %+v", s)
	}
	f.now = f.now.Add(apiHostSilent)
	if s := f.l.status(); s == nil || s.OK || s.Error == "" {
		t.Fatalf("the host never answered: %+v", s)
	}
	write := func(st hostchan.APILockStatus) {
		raw, _ := json.Marshal(st)
		if err := os.WriteFile(filepath.Join(f.dir, "apilock", "status.json"), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(hostchan.APILockStatus{On: true, OK: true, At: f.now.Unix()})
	if s := f.l.status(); s == nil || !s.OK {
		t.Fatalf("the host put the rule in: %+v", s)
	}
	write(hostchan.APILockStatus{Error: "nft is not installed", At: f.now.Unix()})
	if s := f.l.status(); s == nil || s.Error != "nft is not installed" {
		t.Fatalf("the host's error: %+v", s)
	}
}

// The listener closes a refused connection at once and hands the others to the server.
func TestAPIListenerRefusesStrangers(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var refuse atomic.Bool
	refuse.Store(true)
	ln := &apiListener{Listener: inner, admit: func(netip.Addr) bool { return !refuse.Load() }}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()
	c, err := net.Dial("tcp", inner.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil || os.IsTimeout(err) {
		t.Fatalf("a refused connection: %v, want it closed", err)
	}
	c.Close()
	select {
	case <-accepted:
		t.Fatal("a refused connection reached the server")
	default:
	}
	refuse.Store(false)
	c, err = net.Dial("tcp", inner.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	select {
	case a := <-accepted:
		a.Close()
	case <-time.After(5 * time.Second):
		t.Fatal("an admitted connection did not reach the server")
	}
}

// A panel with a domain is let in by the addresses the domain leads to, looked up once a
// minute: one that moved with its domain does not wait for the lock to lift. An address
// literal is taken at once; a name that leads into a private network lets nobody in.
func TestAPILockLetsInThePanelsName(t *testing.T) {
	f := newLockFixture(t)
	answer := []netip.Addr{netip.MustParseAddr("198.51.100.50"), netip.MustParseAddr("10.0.0.7")}
	f.l.resolve = func(context.Context, string) ([]netip.Addr, error) { return answer, nil }
	f.l.set([]string{})
	f.l.contact("203.0.113.5:40000") // the panel came: the lock holds
	f.l.setPanel("https://panel.example.com:2053")
	if f.admits("198.51.100.50") {
		t.Fatal("let in before the name was looked up")
	}
	f.l.tick()
	if !f.admits("198.51.100.50") || f.admits("10.0.0.7") || f.admits("192.0.2.9") {
		t.Fatal("the name's addresses")
	}
	if got := f.asked(); !slices.Contains(got, "198.51.100.50/32") || slices.Contains(got, "10.0.0.7/32") {
		t.Fatalf("the host's rule: %v", got)
	}
	// The panel moved with its domain: a minute later the new address gets in.
	answer = []netip.Addr{netip.MustParseAddr("198.51.100.60")}
	f.now = f.now.Add(30 * time.Second)
	f.l.tick()
	if f.admits("198.51.100.60") {
		t.Fatal("looked up again within the minute")
	}
	f.now = f.now.Add(time.Minute)
	f.l.contact("203.0.113.5:40000")
	f.l.tick()
	if !f.admits("198.51.100.60") || f.admits("198.51.100.50") {
		t.Fatal("the name's new address")
	}
	f.l.setPanel("https://203.0.113.77:2053")
	if !f.admits("203.0.113.77") {
		t.Fatal("an address literal")
	}
}
