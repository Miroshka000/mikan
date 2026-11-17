package node

import (
	"net"
	"strings"

	"mikan/internal/nodeapi"
)

// nameRoute is where the connections that ask for one name go. Several REALITY inbounds may
// share the name when the router tells them apart by key (SNIRouter.ByKey): a client goes to
// the one whose key made its ClientHello, anyone else to the route's own backend.
type nameRoute struct {
	to    string       // the backend when no key says otherwise, or the only one
	keyed []keyedRoute // the REALITY inbounds told apart by key; empty: to alone
}

// keyedRoute is one REALITY inbound of a shared name.
type keyedRoute struct {
	addr string
	key  *realityKey
}

// routeTarget is one inbound a name is routed to, in the order of the panel's routes.
type routeTarget struct {
	addr string
	key  *realityKey // nil: no REALITY inbound, or its key does not parse
}

// pick is the backend for a connection that sent hello: the REALITY inbound whose key made
// it, else the route's own. A ClientHello that is not whole or does not parse goes there too.
func (rt nameRoute) pick(hello []byte) string {
	if len(rt.keyed) == 0 {
		return rt.to
	}
	msg, st := joinHello(hello)
	if st != helloDone {
		return rt.to
	}
	h, ok := parseHello(msg)
	if !ok {
		return rt.to
	}
	for _, k := range rt.keyed {
		if k.key.owns(h) {
			return k.addr
		}
	}
	return rt.to
}

// routeOf is the route of a name from the inbounds it is routed to. One name goes to one
// inbound unless byKey: then its REALITY inbounds are told apart by key, and a client no key
// fits goes to the last inbound of the name that is no REALITY one, or to the first REALITY
// one. Without byKey the last inbound of the name takes it, as the routes were always read.
func routeOf(list []routeTarget, byKey bool) nameRoute {
	if len(list) == 1 || !byKey {
		return nameRoute{to: list[len(list)-1].addr}
	}
	var rt nameRoute
	for _, t := range list {
		if t.key != nil {
			rt.keyed = append(rt.keyed, keyedRoute{addr: t.addr, key: t.key})
		} else {
			rt.to = t.addr
		}
	}
	if rt.to == "" {
		rt.to = rt.keyed[0].addr
	}
	if len(rt.keyed) == 1 && rt.keyed[0].addr == rt.to {
		rt.keyed = nil
	}
	return rt
}

// routesOf builds the routes of a router's state: names (lowercase, no trailing dot) to the
// inbounds' ports on loopback, by the ports and configs the inbounds run.
func routesOf(rt *nodeapi.SNIRouter, inbounds []nodeapi.Inbound) map[string]nameRoute {
	ports := make(map[string]string, len(inbounds))
	keys := map[string]*realityKey{}
	for _, in := range inbounds {
		ports[in.Name] = in.Port
		if rt.ByKey {
			if k := realityKeyOf(in.Config); k != nil {
				keys[in.Name] = k
			}
		}
	}
	byName := map[string][]routeTarget{}
	for _, x := range rt.Routes {
		p, ok := ports[x.Inbound]
		if !ok {
			continue
		}
		t := routeTarget{addr: net.JoinHostPort("127.0.0.1", p), key: keys[x.Inbound]}
		for _, n := range x.Names {
			n = sniName(n)
			byName[n] = append(byName[n], t)
		}
	}
	routes := make(map[string]nameRoute, len(byName))
	for n, list := range byName {
		routes[n] = routeOf(list, rt.ByKey)
	}
	return routes
}

// sniName is a name as the router compares it: lowercase, without the trailing dot.
func sniName(n string) string { return strings.ToLower(strings.TrimSuffix(n, ".")) }
