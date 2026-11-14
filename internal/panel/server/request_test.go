package server

import (
	"net/http"
	"net/netip"
	"testing"
)

func TestClientIP(t *testing.T) {
	SetTrustedProxies([]netip.Prefix{netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("2001:db8:ffff::/48")})
	t.Cleanup(func() { SetTrustedProxies(nil) })
	for _, c := range []struct {
		xff, remote string
		trust       bool
		want        string
	}{
		{"", "198.51.100.7:5555", false, "198.51.100.7"},
		{"", "[::ffff:198.51.100.7]:5555", false, "198.51.100.7"},
		{"", "[2001:db8::1]:443", false, "2001:db8::1"},
		{"10.0.0.1, 203.0.113.5", "127.0.0.1:1", true, "203.0.113.5"},
		{"203.0.113.5", "127.0.0.1:1", false, "127.0.0.1"},       // not trusted: the peer
		{"1.2.3.4, not-an-ip", "127.0.0.1:1", true, "127.0.0.1"}, // garbage: the peer
		{"", "unix-socket", false, "unix-socket"},
		{"203.0.113.5", "unix-socket", true, "203.0.113.5"},              // a local socket is a proxy on this server
		{"203.0.113.5", "10.0.0.2:1", true, "203.0.113.5"},               // a proxy on the private network
		{"203.0.113.5", "[::ffff:172.17.0.1]:1", true, "203.0.113.5"},    // a Docker network, on a dual-stack socket
		{"203.0.113.5", "100.64.0.9:1", true, "203.0.113.5"},             // a tunnel's address
		{"203.0.113.5", "198.51.100.7:5555", true, "198.51.100.7"},       // straight from the internet: its own word
		{"203.0.113.5", "[2001:db8::1]:443", true, "2001:db8::1"},        // the same over IPv6
		{"203.0.113.5", "192.0.2.44:5555", true, "203.0.113.5"},          // a network named as a proxy's
		{"203.0.113.5", "[2001:db8:ffff::9]:443", true, "203.0.113.5"},   // and an IPv6 one
		{"203.0.113.5", "[::ffff:192.0.2.44]:5555", true, "203.0.113.5"}, // the named network on a dual-stack socket
		{"203.0.113.5", "192.0.3.44:5555", true, "192.0.3.44"},           // next to the named network, not in it
	} {
		h := http.Header{}
		if c.xff != "" {
			h.Set("X-Forwarded-For", c.xff)
		}
		if got := ClientIP(h, c.remote, c.trust); got != c.want {
			t.Errorf("%+v: %q", c, got)
		}
	}
}

func TestFetchSite(t *testing.T) {
	for _, c := range []struct {
		site, origin string
		want         Site
	}{
		{"same-origin", "", SiteSame},
		{"cross-site", "", SiteCross},
		{"same-site", "", SiteCross},
		{"none", "", SiteUnknown},
		{"", "", SiteUnknown},
		{"", "https://panel.example", SiteSame},
		{"", "https://evil.example", SiteCross},
		{"", "null", SiteCross},
	} {
		h := http.Header{}
		if c.site != "" {
			h.Set("Sec-Fetch-Site", c.site)
		}
		if c.origin != "" {
			h.Set("Origin", c.origin)
		}
		if got := FetchSite(h, "panel.example"); got != c.want {
			t.Errorf("%+v: %v", c, got)
		}
	}
}
