package node

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/metacubex/mihomo/component/sniffer"

	"mikan/internal/nodeapi"
)

// The SNI router: the node's TCP 443 shared by several inbounds (REALITY, VLESS and Trojan
// over TLS, AnyTLS), each listening on loopback with PROXY protocol. It reads the client's
// TLS ClientHello, picks the inbound by the name it asks for and hands the connection over
// with a PROXY v2 header carrying the client's address, so devices, limits and filters see
// the client and not 127.0.0.1. A name no route has goes to the default inbound (a REALITY
// one passes it to its target, as any stranger's).

const (
	// helloWait bounds the wait for the ClientHello; helloMax what is read of it (a
	// record is 16 KiB, a ClientHello with post-quantum keys fits).
	helloWait = 5 * time.Second
	helloMax  = 16<<10 + 5
)

// sniRouter is the running router; nil while the node has none.
type sniRouter struct {
	ln     net.Listener
	log    *slog.Logger
	mu     sync.RWMutex
	routes map[string]string // name (lowercase) → backend address
	def    string            // backend for any other name; "": closed
	stub   map[string]bool   // names that get the front page instead ("": no name)
	front  *frontPage        // the node's front page; nil: none
	gate   helloGate         // how many connections wait for their ClientHello
	wg     sync.WaitGroup
	err    error
}

// How many connections may wait for their ClientHello at once: all of them, and from one
// source (an IPv4 address, an IPv6 /64). A client's hello comes with its first packet, so
// only connections that say nothing pile up here; without a bound each would hold a
// goroutine and a descriptor for helloWait, and enough of them the whole of 443.
const (
	helloSlots  = 4096
	helloPerSrc = 64
)

// helloGate counts the connections waiting for a ClientHello. The zero value is ready.
type helloGate struct {
	mu    sync.Mutex
	total int
	by    map[netip.Addr]int
}

// enter takes a place for a connection from src and says whether there was one.
func (g *helloGate) enter(src netip.Addr) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.total >= helloSlots || g.by[src] >= helloPerSrc {
		return false
	}
	if g.by == nil {
		g.by = map[netip.Addr]int{}
	}
	g.total++
	g.by[src]++
	return true
}

// leave gives the place of a connection from src back.
func (g *helloGate) leave(src netip.Addr) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.total--
	if g.by[src]--; g.by[src] <= 0 {
		delete(g.by, src)
	}
}

// gateKey is the source a connection counts under: an IPv4 address itself, the /64 of an
// IPv6 one, as one subscriber holds the whole of it.
func gateKey(a netip.Addr) netip.Addr {
	a = a.Unmap()
	if a.Is4() {
		return a
	}
	p, err := a.Prefix(64)
	if err != nil {
		return a
	}
	return p.Addr()
}

// route is where a name goes.
func (r *sniRouter) route(name string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if to, ok := r.routes[strings.ToLower(strings.TrimSuffix(name, "."))]; ok {
		return to
	}
	return r.def
}

// stubbed says whether name gets the front page: a name of the stub's, or with no default
// inbound any name no route has. The page must be on.
func (r *sniRouter) stubbed(name string) bool {
	if !r.front.on() {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := strings.ToLower(strings.TrimSuffix(name, "."))
	if _, ok := r.routes[n]; ok {
		return false
	}
	return r.stub[n] || r.def == "" && len(r.stub) > 0
}

// set replaces the routes: names to inbound ports on loopback, by the ports the inbounds run.
func (r *sniRouter) set(rt *nodeapi.SNIRouter, ports map[string]string) {
	routes := map[string]string{}
	for _, x := range rt.Routes {
		p, ok := ports[x.Inbound]
		if !ok {
			continue
		}
		for _, n := range x.Names {
			routes[strings.ToLower(strings.TrimSuffix(n, "."))] = net.JoinHostPort("127.0.0.1", p)
		}
	}
	stub := map[string]bool{}
	for _, n := range rt.Stub {
		stub[strings.ToLower(strings.TrimSuffix(n, "."))] = true
	}
	def := ""
	if p, ok := ports[rt.Default]; ok {
		def = net.JoinHostPort("127.0.0.1", p)
	}
	r.mu.Lock()
	r.routes, r.def = routes, def
	r.stub = stub
	r.mu.Unlock()
}

func (r *sniRouter) serve(admit func(netip.Addr) bool) {
	for {
		c, err := r.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			time.Sleep(50 * time.Millisecond)
			continue
		}
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			r.handle(c, admit)
		}()
	}
}

func (r *sniRouter) handle(c net.Conn, admit func(netip.Addr) bool) {
	defer c.Close()
	src, ok := addrPort(c.RemoteAddr())
	if !ok || admit != nil && !admit(src.Addr()) {
		return
	}
	// A stranger is held for its ClientHello only while there is room: a flood of opened
	// and silent connections is turned away at once, and the clients' hellos keep theirs.
	key := gateKey(src.Addr())
	if !r.gate.enter(key) {
		return
	}
	hello, name := readHello(c)
	r.gate.leave(key)
	if r.stubbed(name) {
		r.front.take(c, hello)
		return
	}
	to := r.route(name)
	if to == "" {
		return
	}
	up, err := net.DialTimeout("tcp", to, 5*time.Second)
	if err != nil {
		return
	}
	defer up.Close()
	dst, _ := addrPort(c.LocalAddr())
	if _, err := up.Write(append(proxyV2(src, dst), hello...)); err != nil {
		return
	}
	relay(c, up)
}

// readHello reads the ClientHello (all that came of it) and the name it asks for; "" when
// it is no TLS or has no SNI.
func readHello(c net.Conn) ([]byte, string) {
	_ = c.SetReadDeadline(time.Now().Add(helloWait))
	defer c.SetReadDeadline(time.Time{})
	buf := make([]byte, 0, 2048)
	tmp := make([]byte, 4096)
	for len(buf) < helloMax {
		n, err := c.Read(tmp)
		buf = append(buf, tmp[:n]...)
		// A TLS record starts with a handshake: anything else is no ClientHello at all.
		if len(buf) > 0 && buf[0] != 0x16 {
			return buf, ""
		}
		if name, serr := sniffer.SniffTLS(buf); serr == nil && name != nil {
			return buf, *name
		}
		// A whole ClientHello with no name in it is done with at once, not waited on for
		// the rest of helloWait.
		if err != nil || helloWhole(buf) {
			return buf, ""
		}
	}
	return buf, ""
}

// helloWhole says whether buf holds a whole ClientHello in its first record: the record's
// length and the handshake message's both fit.
func helloWhole(buf []byte) bool {
	if len(buf) < 9 || buf[0] != 0x16 || buf[5] != 0x01 {
		return false
	}
	rec := int(buf[3])<<8 | int(buf[4])
	msg := int(buf[6])<<16 | int(buf[7])<<8 | int(buf[8])
	return 4+msg <= rec && len(buf) >= 5+rec
}

// proxyV2 is a PROXY protocol v2 header for a TCP connection from src to dst.
func proxyV2(src, dst netip.AddrPort) []byte {
	sig := []byte("\r\n\r\n\x00\r\nQUIT\n")
	var b bytes.Buffer
	b.Write(sig)
	b.WriteByte(0x21) // version 2, PROXY
	s, d := src.Addr().Unmap(), dst.Addr().Unmap()
	if s.Is4() && d.Is4() {
		b.WriteByte(0x11) // TCP over IPv4
		_ = binary.Write(&b, binary.BigEndian, uint16(12))
		b.Write(s.AsSlice())
		b.Write(d.AsSlice())
	} else {
		b.WriteByte(0x21) // TCP over IPv6
		_ = binary.Write(&b, binary.BigEndian, uint16(36))
		s16, d16 := s.As16(), d.As16()
		b.Write(s16[:])
		b.Write(d16[:])
	}
	_ = binary.Write(&b, binary.BigEndian, src.Port())
	_ = binary.Write(&b, binary.BigEndian, dst.Port())
	return b.Bytes()
}

func addrPort(a net.Addr) (netip.AddrPort, bool) {
	if t, ok := a.(*net.TCPAddr); ok {
		return t.AddrPort(), true
	}
	ap, err := netip.ParseAddrPort(a.String())
	return ap, err == nil
}

// relay copies both ways until one side is done, then closes the other's write side.
func relay(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = dst.Close()
		}
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	<-done
}

// close stops accepting and waits for nothing: the relays end with their connections.
func (r *sniRouter) close() {
	_ = r.ln.Close()
}

// setRouter starts, changes or stops the router as the state says. ports are the inbounds'
// ports as rendered. An error to bind is kept for Health and tried again at the next Apply.
func (e *Engine) setRouter(rt *nodeapi.SNIRouter, ports map[string]string) {
	e.routerMu.Lock()
	defer e.routerMu.Unlock()
	if rt == nil {
		if e.router != nil {
			e.router.close()
			e.router = nil
		}
		e.routerStatus.Store(nil)
		return
	}
	listen := rt.Listen
	if listen == "" {
		listen = ":443"
	}
	if e.router != nil && e.router.ln.Addr().String() != "" && e.routerAddr == listen {
		e.router.set(rt, ports)
		return
	}
	if e.router != nil {
		e.router.close()
		e.router = nil
	}
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", listen)
	if err != nil {
		e.routerStatus.Store(&nodeapi.PortStatus{Error: err.Error()})
		e.log.Warn("sni router", "listen", listen, "err", err)
		return
	}
	r := &sniRouter{ln: ln, log: e.log, front: e.front}
	r.set(rt, ports)
	e.router, e.routerAddr = r, listen
	e.routerStatus.Store(&nodeapi.PortStatus{OK: true})
	go r.serve(e.Reg.admitFrom)
}
