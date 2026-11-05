package node

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/metacubex/mihomo/adapter/outbound"
	"github.com/metacubex/mihomo/component/egress"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/hub/executor"
	"github.com/metacubex/mihomo/tunnel"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/presets"
)

// The way out of a node: its rules keep users off the server's private networks, and the
// core's guard off the server's own addresses but for the ports the panel lists. The targets
// here are on the loopback, the one address of this host a test can count on; test/slice
// does the same on an address of a network interface, with a real panel beside the node.

var loopback = netip.MustParseAddr("127.0.0.1")

// The rules close the private networks without the guard's help, and MIKAN_ALLOW_PRIVATE,
// which test setups depend on, opens them whatever the panel lists: with nothing left open
// the guard would refuse the loopback if it were on.
func TestRulesDecideWhatAUserReaches(t *testing.T) {
	port := echoTCP(t)
	for _, tc := range []struct {
		name         string
		allowPrivate bool
		ports        []int
		want         bool
	}{
		{"private addresses allowed", true, []int{}, true},
		{"private addresses closed", false, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := newNodeUser(t, tc.allowPrivate, nil)
			u.apply(t, tc.ports)
			if got := u.echoes(t, &C.Metadata{DstIP: loopback, DstPort: port}); got != tc.want {
				t.Fatalf("the target answered: %v, want %v", got, tc.want)
			}
		})
	}
}

// A user reaches the ports of the list on the server's own address and no other, and the
// list reaches a node that is running: the core reads its config's section only at the start.
func TestGuardLeavesOpenOnlyTheListedPorts(t *testing.T) {
	a, b := echoTCP(t), echoTCP(t)
	u := newNodeUser(t, false, func() ([]netip.Addr, error) { return []netip.Addr{loopback}, nil })
	u.apply(t, []int{int(a)})
	onlyTheGuard(t)

	reaches := func(port uint16) bool { return u.echoes(t, &C.Metadata{DstIP: loopback, DstPort: port}) }
	check := func(state string, wantA, wantB bool) {
		t.Helper()
		if gotA, gotB := reaches(a), reaches(b); gotA != wantA || gotB != wantB {
			t.Fatalf("%s: the first port answered %v (want %v), the second %v (want %v)", state, gotA, wantA, gotB, wantB)
		}
	}
	check("the first port listed", true, false)
	if !u.echoes(t, &C.Metadata{Host: "localhost", DstPort: a}) || u.echoes(t, &C.Metadata{Host: "localhost", DstPort: b}) {
		t.Fatal("a name that resolves to the server's address must go by the same list")
	}
	u.apply(t, []int{int(b)})
	check("the second port listed", false, true)
	u.apply(t, []int{})
	check("nothing listed", false, false)
	// A panel that sends no list knows nothing of the guard: as before it.
	u.apply(t, nil)
	check("an older panel", true, true)
}

// The list opens the port for UDP as well, and the others stay closed: the guard checks
// every datagram, on a session that goes on.
func TestGuardGoesByTheListOverUDP(t *testing.T) {
	a, b := echoUDP(t), echoUDP(t)
	u := newNodeUser(t, false, func() ([]netip.Addr, error) { return []netip.Addr{loopback}, nil })
	u.apply(t, []int{int(a)})
	onlyTheGuard(t)
	if !u.echoesUDP(t, a) {
		t.Fatal("a port of the list must answer over UDP")
	}
	if u.echoesUDP(t, b) {
		t.Fatal("a port off the list answered over UDP")
	}
}

// An address that comes up on the server after the last state gets the open ports too, with
// no new state: the panel beside the node must not drop out of reach until the next change.
func TestGuardFollowsTheServersAddresses(t *testing.T) {
	port := echoTCP(t)
	var own []netip.Addr
	var fail error
	u := newNodeUser(t, false, func() ([]netip.Addr, error) { return own, fail })
	u.apply(t, []int{int(port)})
	onlyTheGuard(t)
	reaches := func() bool { return u.echoes(t, &C.Metadata{DstIP: loopback, DstPort: port}) }
	if reaches() {
		t.Fatal("no address of the server is known yet, and the port answered")
	}
	own = []netip.Addr{loopback}
	u.e.TickGuard()
	if !reaches() {
		t.Fatal("the new address did not get the port")
	}
	// A failed look at the interfaces changes nothing: the panel stays in reach.
	own, fail = nil, net.ErrClosed
	u.e.TickGuard()
	if !reaches() {
		t.Fatal("a failed look at the interfaces took the port away")
	}
	u.apply(t, []int{int(port)})
	if !reaches() {
		t.Fatal("a state applied while the interfaces cannot be read took the port away")
	}
}

// A tick leaves a guard the state turned off alone.
func TestGuardTickKeepsItOff(t *testing.T) {
	for _, tc := range []struct {
		name         string
		allowPrivate bool
		ports        []int
	}{
		{"an older panel", false, nil},
		{"private addresses allowed", true, []int{443}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := newNodeUser(t, tc.allowPrivate, func() ([]netip.Addr, error) { return []netip.Addr{loopback}, nil })
			u.apply(t, tc.ports)
			u.e.TickGuard()
			if o := egress.CurrentOptions(); o.Enable || len(o.AllowAddrPorts) != 0 {
				t.Fatalf("the guard after a tick: %+v", o)
			}
		})
	}
}

// A node that restarts has the guard as it was, before the panel's next push: the saved state
// keeps the list, an empty one too.
func TestGuardComesBackWithTheSavedState(t *testing.T) {
	own := netip.MustParseAddr("192.0.2.7")
	for _, tc := range []struct {
		name  string
		ports []int
		on    bool
		allow []netip.AddrPort
	}{
		{"none sent", nil, false, nil},
		{"nothing open", []int{}, true, nil},
		{"one port", []int{443}, true, []netip.AddrPort{netip.AddrPortFrom(own, 443)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			first, err := Start(Options{DataDir: dir, Version: "test", Log: quiet()})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := first.Apply(nodeapi.DesiredState{Revision: 1, OwnPorts: tc.ports}); err != nil {
				t.Fatal(err)
			}
			egress.SetOptions(egress.Options{Enable: !tc.on})
			again, err := Start(Options{DataDir: dir, Version: "test", Log: quiet()})
			if err != nil {
				t.Fatal(err)
			}
			again.ownAddrs = func() ([]netip.Addr, error) { return []netip.Addr{own}, nil }
			again.Restore()
			t.Cleanup(func() { _, _ = again.Apply(nodeapi.DesiredState{Revision: 99}) })
			if o := egress.CurrentOptions(); o.Enable != tc.on || !slices.Equal(o.AllowAddrPorts, tc.allow) {
				t.Fatalf("the guard after a restart: %+v, want on %v with %v", o, tc.on, tc.allow)
			}
		})
	}
}

func TestGuardOptions(t *testing.T) {
	v4, v6 := netip.MustParseAddr("198.51.100.7"), netip.MustParseAddr("2001:db8::7")
	own := []netip.Addr{v4, v6}
	pair := netip.AddrPortFrom
	for _, tc := range []struct {
		name         string
		ports        []int
		allowPrivate bool
		on           bool
		allow        []netip.AddrPort
	}{
		{name: "an older panel sends no list", ports: nil, on: false},
		{name: "private addresses allowed", ports: []int{443}, allowPrivate: true, on: false},
		{name: "nothing open", ports: []int{}, on: true},
		{name: "every address gets every port", ports: []int{80, 443}, on: true,
			allow: []netip.AddrPort{pair(v4, 80), pair(v4, 443), pair(v6, 80), pair(v6, 443)}},
		// As with the egress rules, what cannot be a port is left out, not the state refused.
		{name: "not a port", ports: []int{-1, 0, 65536, 443}, on: true, allow: []netip.AddrPort{pair(v4, 443), pair(v6, 443)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := guardOptions(tc.ports, tc.allowPrivate, own)
			if o.Enable != tc.on || !slices.Equal(o.AllowAddrPorts, tc.allow) || len(o.AllowIPs) != 0 {
				t.Fatalf("%+v, want on %v with %v", o, tc.on, tc.allow)
			}
		})
	}
}

// The addresses a port is opened on are the public ones: the rules and the guard keep the
// private ones closed, and opening one would go past the guard.
func TestInterfaceAddrsArePublic(t *testing.T) {
	addrs, err := interfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range addrs {
		if !a.IsGlobalUnicast() || a.IsPrivate() || a.Is4In6() {
			t.Errorf("%s is not a public address", a)
		}
	}
	if !slices.IsSortedFunc(addrs, netip.Addr.Compare) {
		t.Errorf("not sorted: %v", addrs)
	}
}

// The core guards the types of inbound on a list of its own, and a type left off it would
// let its users past: every protocol the panel offers must be on it. A new one fails here
// until it is added below.
func TestGuardCoversEveryProtocol(t *testing.T) {
	types := map[string]C.Type{
		"vless": C.VLESS, "trojan": C.TROJAN, "hysteria2": C.HYSTERIA2, "tuic": C.TUIC, "anytls": C.ANYTLS,
		"trusttunnel": C.TRUSTTUNNEL, "shadowquic": C.SHADOWQUIC, "mieru": C.MIERU, "shadowsocks": C.SHADOWSOCKS,
		"sudoku": C.SUDOKU, "snell": C.SNELL,
	}
	for _, p := range presets.All {
		if p.ID == presets.Custom {
			continue // the admin's template picks one of the same types
		}
		typ, known := types[p.Type]
		if !known {
			t.Errorf("%s: the type %q is not in the table", p.ID, p.Type)
			continue
		}
		if !egress.IsServerInbound(typ) {
			t.Errorf("%s: the core's guard does not cover %q", p.ID, p.Type)
		}
	}
}

// nodeUser is a running node with a user on it, who sends through it to any address.
type nodeUser struct {
	e       *Engine
	inbound nodeapi.Inbound
	client  *outbound.ShadowSocks
}

// newNodeUser starts a node, with own in place of the server's addresses when it is not nil.
func newNodeUser(t *testing.T, allowPrivate bool, own func() ([]netip.Addr, error)) *nodeUser {
	t.Helper()
	e, err := Start(Options{DataDir: t.TempDir(), Version: "test", AllowPrivate: allowPrivate, Log: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	e.ownAddrs = own
	t.Cleanup(func() { _, _ = e.Apply(nodeapi.DesiredState{Revision: 99}) })

	// Shadowsocks-2022 with ChaCha20: one key for the inbound, no users to set up. Its UDP goes
	// inside TCP, as the panel's Shadowsocks-2022 takes it.
	const cipher = "2022-blake3-chacha20-poly1305"
	password := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	port := freePort(t)
	inboundPort, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	client, err := outbound.NewShadowSocks(outbound.ShadowSocksOption{Name: "client", Server: "127.0.0.1", Port: inboundPort, Cipher: cipher, Password: password, UDPOverTCP: true})
	if err != nil {
		t.Fatal(err)
	}
	return &nodeUser{e: e, client: client, inbound: nodeapi.Inbound{
		Name: "ss", Listen: "127.0.0.1", Port: port,
		Config: []byte(`{"type":"shadowsocks","cipher":"` + cipher + `","password":"` + password + `"}`),
	}}
}

// apply gives the node a state with the user's inbound and the panel's list of ports.
func (u *nodeUser) apply(t *testing.T, ports []int) {
	t.Helper()
	res, err := u.e.Apply(nodeapi.DesiredState{Revision: 1, Inbounds: []nodeapi.Inbound{u.inbound}, OwnPorts: ports})
	if err != nil {
		t.Fatal(err)
	}
	if st := statusOf(res.Listeners)["ss"]; !st.OK {
		t.Fatalf("the inbound does not run: %+v", st)
	}
}

const through = "through the node"

// echoes says whether what the user sends to the destination comes back.
func (u *nodeUser) echoes(t *testing.T, dst *C.Metadata) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dst.NetWork = C.TCP
	conn, err := u.client.DialContext(ctx, dst)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte(through)); err != nil {
		return false
	}
	got := make([]byte, len(through))
	if _, err := io.ReadFull(conn, got); err != nil {
		return false
	}
	return string(got) == through
}

// echoesUDP says whether a datagram the user sends to the port on the loopback comes back.
func (u *nodeUser) echoesUDP(t *testing.T, port uint16) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pc, err := u.client.ListenPacketContext(ctx, &C.Metadata{NetWork: C.UDP, DstIP: loopback, DstPort: port})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	// A refused datagram is dropped with no word back: a short wait is the answer.
	if err := pc.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := pc.WriteTo([]byte(through), net.UDPAddrFromAddrPort(netip.AddrPortFrom(loopback, port))); err != nil {
		return false
	}
	got := make([]byte, 2*len(through))
	n, _, err := pc.ReadFrom(got)
	return err == nil && string(got[:n]) == through
}

// onlyTheGuard takes away the rules that close the loopback, so that the guard alone decides
// on it, as it does on a public address of the server. They stay away while the node's own
// rules do not change.
func onlyTheGuard(t *testing.T) {
	t.Helper()
	cfg, err := executor.ParseWithBytes([]byte(`{"rules":["MATCH,DIRECT"]}`))
	if err != nil {
		t.Fatal(err)
	}
	tunnel.UpdateRules(cfg.Rules, cfg.SubRules, cfg.RuleProviders)
}

// echoTCP is a listener on the loopback that sends back what it gets; it returns its port.
func echoTCP(t *testing.T) uint16 {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	return uint16(l.Addr().(*net.TCPAddr).Port)
}

// echoUDP is echoTCP over UDP.
func echoUDP(t *testing.T) uint16 {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteTo(buf[:n], from)
		}
	}()
	return uint16(pc.LocalAddr().(*net.UDPAddr).Port)
}
