package scan

import "testing"

func TestWeak(t *testing.T) {
	for name, want := range map[string]bool{
		"shop.example.org":       false,
		"www.example.com":        false,
		"example.com":            false,
		"mailer.example.com":     false, // a label that only starts like one
		"mail.example.com":       true,
		"AutoConfig.Example.Top": true,
		"autodiscover.host.net":  true,
		"vpn.example.com":        true,
		"home.duckdns.org":       true,
		"duckdns.org":            true,
		"notduckdns.org":         false,
		"a.b.sslip.io.":          true,
		"cam.keenetic.pro":       true,
	} {
		if got := Weak(name); got != want {
			t.Errorf("Weak(%q) = %v, want %v", name, got, want)
		}
	}
}

// A certificate's name to send is the first one people open, whatever the order; a
// certificate of weak names alone still gives its first.
func TestPickName(t *testing.T) {
	for _, c := range []struct {
		names []string
		want  string
	}{
		{[]string{"autoconfig.example.top", "autodiscover.example.top", "mail.example.top"}, "autoconfig.example.top"},
		{[]string{"mail.example.top", "Shop.Example.top"}, "shop.example.top"},
		{[]string{"*.example.com", "example.com"}, "example.com"},
		{[]string{"*.example.com", "localhost", "203.0.113.7"}, ""},
		{nil, ""},
	} {
		if got := pickName(c.names); got != c.want {
			t.Errorf("pickName(%v) = %q, want %q", c.names, got, c.want)
		}
	}
}
