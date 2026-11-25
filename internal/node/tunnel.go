package node

import (
	"net"
	"sync/atomic"
	"time"

	N "github.com/metacubex/mihomo/common/net"
	C "github.com/metacubex/mihomo/constant"

	"mikan/internal/nodeapi"
)

// Tunnel sits between the core's listeners and its tunnel. The core counts, limits and
// closes users by itself (core.go); what passes through here is what spans the inbounds
// of a slot: the device limit and the torrent blocker, plus the ingress filter. The
// user's speed is the core's too, worked out by the registry (shaper.go).
type Tunnel struct {
	inner        C.Tunnel
	reg          *Registry
	paymentHosts atomic.Pointer[paymentTargets]
}

func (t *Tunnel) HandleTCPConn(conn net.Conn, m *C.Metadata) {
	// REALITY dials its dest through this tunnel for every handshake; these carry no
	// user and must pass untouched or no client can connect (S-01a). They also bypass
	// the REJECT rules for private ranges, so a self-hosted dest keeps working.
	if m.Type == C.INNER {
		defer conn.Close()
		remote, err := net.DialTimeout("tcp", m.RemoteAddress(), 10*time.Second)
		if err != nil {
			return
		}
		defer remote.Close()
		N.Relay(conn, remote)
		return
	}
	// Another node of the panel relays its users' traffic: the listener checked its key,
	// and the source node has already counted and limited them. The rules still apply.
	if m.InName == nodeapi.RelayListener {
		t.inner.HandleTCPConn(conn, m)
		return
	}
	if !t.reg.admitFrom(m.SrcIP) {
		_ = conn.Close()
		return
	}
	ip := m.SrcIP.Unmap().String()
	s := t.reg.admitIn(m.InUser, m.InName, ip)
	if s == nil {
		_ = conn.Close()
		return
	}
	if t.reg.torrentWatch(s) {
		bc := N.NewBufferedConn(conn)
		conn = bc
		if kind := torrentTCP(peekFirst(bc)); kind != "" {
			t.reg.caught(s, ip, m.InName, "tcp", kind, m.RemoteAddress())
			_ = conn.Close()
			return
		}
	}
	if paymentUser(m.InUser) {
		conn = t.paymentTCP(conn, m)
		if conn == nil {
			return
		}
	}
	// The core holds the connection to the user's speed (shaper.go) and counts it to its
	// site as it ends (sites.go).
	t.inner.HandleTCPConn(conn, m)
}

// peekFirst returns the bytes the client sent first, waiting torrentPeekWait at most;
// nil when it sent nothing by then, or the connection cannot be given a deadline and
// waiting could hang it.
func peekFirst(bc *N.BufferedConn) []byte {
	if bc.SetReadDeadline(time.Now().Add(torrentPeekWait)) != nil {
		return nil
	}
	_, err := bc.Peek(1)
	_ = bc.SetReadDeadline(time.Time{})
	if err != nil {
		return nil
	}
	// Peek(1) has buffered what arrived with the first byte; this one cannot block.
	b, _ := bc.Peek(bc.Buffered())
	return b
}

func (t *Tunnel) HandleUDPPacket(p C.UDPPacket, m *C.Metadata) {
	if m.Type == C.INNER || m.InName == nodeapi.RelayListener {
		t.inner.HandleUDPPacket(p, m)
		return
	}
	if paymentUser(m.InUser) || !t.reg.admitFrom(m.SrcIP) {
		p.Drop()
		return
	}
	ip := m.SrcIP.Unmap().String()
	s := t.reg.admitIn(m.InUser, m.InName, ip)
	if s == nil {
		p.Drop()
		return
	}
	if t.reg.torrentWatch(s) {
		if kind := torrentUDP(p.Data()); kind != "" {
			t.reg.caught(s, ip, m.InName, "udp", kind, m.RemoteAddress())
			p.Drop()
			return
		}
	}
	// The core holds the session to the user's speed, both ways, and counts it to its site
	// as it ends, like a TCP connection.
	t.inner.HandleUDPPacket(p, m)
}

func (t *Tunnel) NatTable() C.NatTable { return t.inner.NatTable() }
