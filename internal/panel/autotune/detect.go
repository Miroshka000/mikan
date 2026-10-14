// Package autotune keeps inbounds reachable without the admin. It moves an inbound to
// another port when devices stop reaching it while they still reach the node's other
// inbounds, i.e. the port is blocked on the way (the RU DPI does this per server and
// port), and it replaces a REALITY target that stopped working. Both are switched on in
// settings and per inbound; clients learn the change from their next subscription update.
package autotune

import (
	"net/netip"
	"slices"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
)

// User is what the detector needs to know about one slot: a subscription's own keys,
// or the keys of one of its bound devices.
type User struct {
	Inbounds []int64 // inbound ids in the user's profile; empty = all
	// SubFetched: when each of the user's devices (by IP) last took the profile. A device
	// that did not take it after an inbound changed may still use the old port.
	SubFetched map[string]int64
	// Fetched: when the one bound device these keys belong to last took its profile, from
	// wherever it was. Zero for the subscription's own keys, which several apps share.
	Fetched int64
}

// Evidence is one node's inbounds and what its devices reached lately.
type Evidence struct {
	Inbounds []db.Inbound    // the node's enabled inbounds
	Users    map[string]User // by slot name
	Activity nodeapi.Activity
	// Reach: when each slot last got through to each inbound (by id), remembered for weeks.
	// A device is cut off only from an inbound it has reached before: an app that does not
	// speak the protocol (Happ and TUIC), or a profile without the inbound, never reaches it.
	Reach map[string]map[int64]int64
	Since time.Time // older activity does not count
}

// Verdict counts, for one inbound, the devices that try all of the node's inbounds.
type Verdict struct {
	Blocked int // did not reach it
	Reached int // reached it
	// Networks is how many networks (see netOf) the blocked devices are on.
	Networks int
}

// CutOff: devices that reach everything else do not reach this inbound. A few devices
// failing next to many that get through are their own networks' problem, and so is one
// network: a device with a second VPN beside the app, or a home router that drops one
// port, would move the port for everyone (GitHub issue #67).
func (v Verdict) CutOff() bool { return v.Networks >= 2 && 4*v.Blocked >= v.Reached }

// Detect finds inbounds the node's devices cannot reach. Only devices that clearly try
// every inbound count: a Clash profile's url-test group checks all proxies every few
// minutes, a client with one chosen link does not. A device counts for an inbound when
//   - the inbound is in its profile and this device took the profile after the inbound's
//     last change (otherwise it may still knock on the old port);
//   - it reached at least k other inbounds of the node, one of them on the same network:
//     a network that blocks all UDP is not a blocked port.
//
// It is cut off from the inbound only if it got through to it before, and no device of its
// network gets through now: then the failure is the device's own (its app, its profile).
func Detect(e Evidence) map[int64]Verdict {
	out := map[int64]Verdict{}
	k := min(3, len(e.Inbounds)-1)
	if k < 2 {
		return out
	}
	network := make(map[string]string, len(e.Inbounds))
	for _, in := range e.Inbounds {
		network[in.Name] = domain.InboundNetwork(in)
	}
	since := e.Since.Unix()
	reachedFrom := map[string]map[string]bool{} // inbound name → networks it was reached from
	blockedOn := map[int64]map[string]bool{}    // inbound id → networks of its blocked devices
	for _, c := range e.Activity.Clients {
		for name, at := range c.Seen {
			if at >= since {
				if reachedFrom[name] == nil {
					reachedFrom[name] = map[string]bool{}
				}
				reachedFrom[name][netOf(c.IP)] = true
			}
		}
	}
	for _, c := range e.Activity.Clients {
		u, ok := e.Users[c.Slot]
		if !ok {
			continue
		}
		recent := map[string]bool{}
		for name, at := range c.Seen {
			if _, known := network[name]; known && at >= since {
				recent[name] = true
			}
		}
		fetched := u.SubFetched[c.IP]
		if u.Fetched > 0 {
			fetched = u.Fetched
		}
		for _, x := range e.Inbounds {
			if len(u.Inbounds) > 0 && !slices.Contains(u.Inbounds, x.ID) || fetched < x.UpdatedAt {
				continue
			}
			others, sameNet := 0, false
			for name := range recent {
				if name != x.Name {
					others++
					sameNet = sameNet || network[name] == network[x.Name]
				}
			}
			if others < k || !sameNet {
				continue
			}
			v := out[x.ID]
			switch {
			case recent[x.Name]:
				v.Reached++
			case e.Reach[c.Slot][x.ID] > 0 && !reachedFrom[x.Name][netOf(c.IP)]:
				v.Blocked++
				if blockedOn[x.ID] == nil {
					blockedOn[x.ID] = map[string]bool{}
				}
				blockedOn[x.ID][netOf(c.IP)] = true
				v.Networks = len(blockedOn[x.ID])
			default:
				continue
			}
			out[x.ID] = v
		}
	}
	return out
}

// netOf is the network of a device's address for comparing devices: its /24, or /64 for
// IPv6. An address that does not parse is a network of its own.
func netOf(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	a = a.Unmap()
	bits := 64
	if a.Is4() {
		bits = 24
	}
	p, _ := a.Prefix(bits)
	return p.String()
}

// FreePorts returns the ports a blocked inbound of the node may move to on network: free
// on the node's port map and not given up lately, of domain.PortPool, and past it when
// anyPort (settings.AnyPort) is on and the pool is full.
func FreePorts(ports domain.PortMap, network string, abandoned map[string]bool, anyPort bool) []string {
	return ports.PickPorts(network, anyPort, abandoned)
}
