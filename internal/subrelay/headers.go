package subrelay

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Relay metadata is accepted only over the authenticated panel-to-edge connection
// and is never forwarded to subscription clients.
const RelayErrorHeader = "X-Mikan-Relay-Error"
const RelayActionHeader = "X-Mikan-Relay-Action"
const RelayInvalidateHeader = "X-Mikan-Relay-Invalidate"

func forwardedHeaders(source http.Header) http.Header {
	h := source.Clone()
	for _, name := range strings.Split(h.Get("Connection"), ",") {
		h.Del(strings.TrimSpace(name))
	}
	for name := range h {
		n := strings.ToLower(name)
		if n == "cookie" || n == "authorization" || n == "forwarded" || n == "x-real-ip" || n == "cf-connecting-ip" || n == "true-client-ip" || strings.HasPrefix(n, "x-forwarded-") || (strings.HasPrefix(n, "x-mikan-") && n != "x-mikan-features") {
			h.Del(name)
		}
	}
	for _, n := range []string{"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"} {
		h.Del(n)
	}
	return h
}

// SetTrustedProxies enables a selected IP header only for immediate peers in these
// operator-supplied CIDRs. For X-Forwarded-For, walk right to left past trusted hops.
func (p *Proxy) SetTrustedProxies(cidrs, header string) error {
	if header != "X-Forwarded-For" && header != "CF-Connecting-IP" {
		return fmt.Errorf("client IP header must be X-Forwarded-For or CF-Connecting-IP")
	}
	var prefixes []netip.Prefix
	for _, value := range strings.Split(cidrs, ",") {
		if strings.TrimSpace(value) == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("invalid trusted proxy CIDR")
		}
		prefixes = append(prefixes, prefix)
	}
	p.trustedProxies, p.clientIPHeader = prefixes, header
	return nil
}
func (p *Proxy) trusted(ip netip.Addr) bool {
	for _, prefix := range p.trustedProxies {
		if prefix.Contains(ip.Unmap()) {
			return true
		}
	}
	return false
}
func (p *Proxy) clientIP(r *http.Request) string {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return ""
	}
	if !p.trusted(peer) {
		return peer.Unmap().String()
	}
	value := r.Header.Get(p.clientIPHeader)
	if p.clientIPHeader == "CF-Connecting-IP" {
		if ip, err := netip.ParseAddr(strings.TrimSpace(value)); err == nil {
			return ip.Unmap().String()
		}
		return peer.Unmap().String()
	}
	ips := strings.Split(value, ",")
	for i := len(ips) - 1; i >= 0; i-- {
		ip, err := netip.ParseAddr(strings.TrimSpace(ips[i]))
		if err != nil {
			return peer.Unmap().String()
		}
		peer = ip.Unmap()
		if !p.trusted(peer) {
			break
		}
	}
	return peer.Unmap().String()
}

type relayContextKey struct{}

// WithRelay marks requests authenticated by the panel's dedicated relay endpoint.
func WithRelay(ctx context.Context) context.Context {
	return context.WithValue(ctx, relayContextKey{}, true)
}
func IsRelay(ctx context.Context) bool { yes, _ := ctx.Value(relayContextKey{}).(bool); return yes }
