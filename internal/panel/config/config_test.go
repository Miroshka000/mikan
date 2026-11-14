package config

import (
	"net/netip"
	"slices"
	"testing"
)

// MIKAN_TRUSTED_PROXIES: addresses and networks split by commas; an address is the network
// of itself, a network is cut to its own bits, and a word that is neither stops the start.
func TestTrustedProxiesEnv(t *testing.T) {
	t.Setenv("MIKAN_TRUSTED_PROXIES", " 203.0.113.7, 198.51.100.9/24 ,2001:db8::1, 2001:db8:1::/48,, ::ffff:192.0.2.1 ")
	got, err := prefixesEnv("MIKAN_TRUSTED_PROXIES")
	want := []netip.Prefix{
		netip.MustParsePrefix("203.0.113.7/32"), netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("2001:db8::1/128"), netip.MustParsePrefix("2001:db8:1::/48"), netip.MustParsePrefix("192.0.2.1/32"),
	}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("got %v %v, want %v", got, err, want)
	}
	t.Setenv("MIKAN_TRUSTED_PROXIES", "")
	if got, err := prefixesEnv("MIKAN_TRUSTED_PROXIES"); err != nil || got != nil {
		t.Fatalf("unset: %v %v", got, err)
	}
	t.Setenv("MIKAN_TRUSTED_PROXIES", "203.0.113.7, cloudflare")
	if _, err := prefixesEnv("MIKAN_TRUSTED_PROXIES"); err == nil {
		t.Fatal("a word that is no address passed")
	}
	t.Setenv("MIKAN_TRUSTED_PROXIES", "203.0.113.0/24")
	c, err := FromEnv()
	if err != nil || len(c.TrustedProxies) != 1 || c.TrustedProxies[0] != netip.MustParsePrefix("203.0.113.0/24") {
		t.Fatalf("FromEnv: %v %v", c.TrustedProxies, err)
	}
}
