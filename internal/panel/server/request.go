package server

import (
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"

	"mikan/internal/netaddr"
)

// proxies are the networks of the proxies in front of the panel that are not on its own
// server or network (SetTrustedProxies); nil: none.
var proxies atomic.Pointer[[]netip.Prefix]

// SetTrustedProxies names the networks whose X-Forwarded-For the panel believes besides
// its own server's and its private networks' (MIKAN_TRUSTED_PROXIES): a CDN's, a proxy on
// another server. It is set once, at start.
func SetTrustedProxies(nets []netip.Prefix) {
	if len(nets) == 0 {
		proxies.Store(nil)
		return
	}
	proxies.Store(&nets)
}

// trustedProxy says whether a connection from peer comes from a proxy the panel believes:
// one on the server itself or on its private network (nginx, HAProxy, a Docker network, a
// tunnel), or one of the networks named with SetTrustedProxies. A client that reaches the
// panel straight from the internet is not: what it writes in X-Forwarded-For is its own
// word.
func trustedProxy(peer netip.Addr) bool {
	switch netaddr.Classify(peer) {
	case netaddr.Loopback, netaddr.Private, netaddr.Shared, netaddr.LinkLocal:
		return true
	}
	if nets := proxies.Load(); nets != nil {
		peer = netaddr.Unwrap(peer)
		for _, n := range *nets {
			if n.Contains(peer) {
				return true
			}
		}
	}
	return false
}

// ClientIP is the address a request came from: the last X-Forwarded-For entry (the one the
// proxy appended) when the panel sits behind a proxy (trustProxy, MIKAN_TRUST_PROXY) and
// the connection comes from one it believes (trustedProxy), else the peer. The header of a
// connection that came straight from the internet is not read: anyone could name any
// address in it, to the rate limits, the log and the devices of a subscription alike. An
// IPv4 address comes as IPv4 even when the socket is dual-stack (::ffff:a.b.c.d), so it
// reads the same to rate limits, to the nodes and to a provider's address list.
func ClientIP(h http.Header, remoteAddr string, trustProxy bool) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	peer, perr := netip.ParseAddr(host)
	// A peer with no address is a local socket: a proxy on this server.
	if trustProxy && (perr != nil || trustedProxy(peer)) {
		if xff := h.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if ip, err := netip.ParseAddr(strings.TrimSpace(parts[len(parts)-1])); err == nil {
				return ip.Unmap().String()
			}
		}
	}
	if perr == nil {
		return peer.Unmap().String()
	}
	return host
}

// Site is where a browser says a request comes from.
type Site int

const (
	// SiteUnknown: no browser said anything (a script, curl, an app), or it was the user's
	// own navigation.
	SiteUnknown Site = iota
	SiteSame
	SiteCross
)

// FetchSite reads Sec-Fetch-Site, or Origin from a browser too old to send it. Callers
// decide what an unknown origin is worth: the API lets scripts through (they need the
// CSRF header anyway), the subscription's browser-only endpoints do not.
func FetchSite(h http.Header, host string) Site {
	switch h.Get("Sec-Fetch-Site") {
	case "same-origin":
		return SiteSame
	case "none":
		return SiteUnknown
	case "":
	default:
		return SiteCross
	}
	origin := h.Get("Origin")
	if origin == "" {
		return SiteUnknown
	}
	if u, err := url.Parse(origin); err == nil && u.Host != "" && u.Host == host {
		return SiteSame
	}
	return SiteCross
}
