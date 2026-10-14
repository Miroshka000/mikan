package auth

import "testing"

// One IPv6 client owns a whole /64: the limiters count the network, not the address, so
// rotating through addresses does not hand out fresh allowances.
func TestClientKey(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.7":                "203.0.113.7",
		"::ffff:203.0.113.7":         "203.0.113.7",
		"2001:db8:1:2:aaaa:bbbb:1:2": "2001:db8:1:2::/64",
		"2001:db8:1:2:cccc:dddd:3:4": "2001:db8:1:2::/64",
		"2001:db8:1:3::1":            "2001:db8:1:3::/64",
		"not an ip":                  "not an ip",
	} {
		if got := ClientKey(in); got != want {
			t.Errorf("ClientKey(%q) = %q, want %q", in, got, want)
		}
	}
}
