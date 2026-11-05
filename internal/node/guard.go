package node

import (
	"math"
	"net"
	"net/netip"
	"slices"

	"github.com/metacubex/mihomo/component/egress"
	"github.com/metacubex/mihomo/component/iface"
	"github.com/metacubex/mihomo/component/publicaddr"
)

// The core's egress guard keeps the node's users off the server itself: its private ranges,
// which the node's rules close as well, and the addresses of its network interfaces. A
// connection to those comes in through the loopback interface, past any firewall that
// closes the port from outside, so a user would reach whatever listens on the server's
// public address. A panel or a site on the server must still open for its clients: the
// ports the panel lists (DesiredState.OwnPorts) stay open on the server's public addresses.
//
// The core takes an exception as an address and a port, and reads its config's section for
// the guard only in ApplyConfig, which runs once (applyBase). So the node publishes the
// exceptions itself: with every state, and when the server's addresses change.

// guardOptions are the guard's settings for the panel's list of ports and the server's public
// addresses. No list means an older panel, which says nothing of what must stay open:
// the guard stays off, as it is with MIKAN_ALLOW_PRIVATE, where the rules let users reach
// the server's private networks.
func guardOptions(ports []int, allowPrivate bool, own []netip.Addr) egress.Options {
	if ports == nil || allowPrivate {
		return egress.Options{}
	}
	o := egress.Options{Enable: true}
	for _, a := range own {
		for _, p := range ports {
			// What cannot be a port is left out, as the egress rules leave out what does
			// not parse: a broken or newer panel must not take the node's users down.
			if p > 0 && p <= math.MaxUint16 {
				o.AllowAddrPorts = append(o.AllowAddrPorts, netip.AddrPortFrom(a, uint16(p)))
			}
		}
	}
	return o
}

// interfaceAddrs are the server's public addresses, as the guard sees them: those on its
// network interfaces that are up, sorted. An address behind NAT is not among them, and the
// guard does not take it for this host's: a connection to it goes out and comes back in
// from outside, where the firewall sees it.
func interfaceAddrs() ([]netip.Addr, error) {
	ifs, err := iface.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []netip.Addr
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 {
			continue
		}
		for _, p := range i.Addresses {
			if a := p.Addr().Unmap(); publicaddr.IsPublicIP(a) {
				out = append(out, a)
			}
		}
	}
	slices.SortFunc(out, netip.Addr.Compare)
	return slices.Compact(out), nil
}

// readOwnAddrs looks at the server's addresses; on a failure, the ones it knew stay, and the
// log says so once, not at every tick. Call with guardMu held.
func (e *Engine) readOwnAddrs() []netip.Addr {
	read := e.ownAddrs
	if read == nil {
		read = interfaceAddrs
	}
	addrs, err := read()
	if err != nil {
		if !e.guardBlind {
			e.log.Warn("egress guard: the server's addresses cannot be read", "err", err)
		}
		e.guardBlind = true
		return e.guardAddrs
	}
	e.guardBlind = false
	return addrs
}

// setGuard publishes the guard's settings for the ports of a new state.
func (e *Engine) setGuard(ports []int) {
	e.guardMu.Lock()
	defer e.guardMu.Unlock()
	e.guardPorts = ports
	if ports != nil && !e.allowPrivate {
		e.guardAddrs = e.readOwnAddrs()
	}
	e.publishGuard()
}

// TickGuard is called every few seconds: an address that came up on the server since the
// last state gets the open ports too, and the panel beside the node stays in reach on it.
func (e *Engine) TickGuard() {
	e.guardMu.Lock()
	defer e.guardMu.Unlock()
	if e.guardPorts == nil || e.allowPrivate {
		return
	}
	addrs := e.readOwnAddrs()
	if slices.Equal(addrs, e.guardAddrs) {
		return
	}
	e.guardAddrs = addrs
	e.publishGuard()
}

// publishGuard gives the core the settings and says so in the log when they changed: the
// refusals themselves are per-connection lines, which stay out of it (connLine). Call with
// guardMu held.
func (e *Engine) publishGuard() {
	o := guardOptions(e.guardPorts, e.allowPrivate, e.guardAddrs)
	egress.SetOptions(o)
	if o.Enable == e.guardOn && slices.Equal(o.AllowAddrPorts, e.guardOpen) {
		return
	}
	e.guardOn, e.guardOpen = o.Enable, o.AllowAddrPorts
	e.log.Info("egress guard", "on", o.Enable, "open", o.AllowAddrPorts)
}
