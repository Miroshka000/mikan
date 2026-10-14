package dnscheck

import "net/netip"

// cloudflareRanges are Cloudflare's published networks (cloudflare.com/ips). A domain with
// the orange cloud on resolves into them: clients and the CA reach Cloudflare's proxy, not
// the server, so neither HTTP-01 nor the VPN protocols work through it.
var cloudflareRanges = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22", "141.101.64.0/18", "108.162.192.0/18",
		"190.93.240.0/20", "188.114.96.0/20", "197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
		"104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
		"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32", "2405:8100::/32", "2a06:98c0::/29", "2c0f:f248::/32",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// Cloudflare says whether ip is one of Cloudflare's: the domain is proxied (orange cloud).
func Cloudflare(ip netip.Addr) bool {
	ip = ip.Unmap()
	for _, p := range cloudflareRanges {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}
