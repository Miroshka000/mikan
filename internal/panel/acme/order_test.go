package acme

import "testing"

// Let's Encrypt refuses a CSR with an IP in the Common Name (badCSR) and issues IP
// certificates only with the shortlived profile; domains stay as they were. The
// subscription domain rides the panel's order as a second SAN, an IP's cannot.
func TestOrder(t *testing.T) {
	for _, tc := range []struct {
		id, sub, profile string
		noCN             bool
		names            []string
	}{
		{"77.238.243.225", "", "shortlived", true, []string{"77.238.243.225"}},
		{"2001:db8::1", "", "shortlived", true, []string{"2001:db8::1"}},
		{"vpn.example.com", "", "", false, []string{"vpn.example.com"}},
		{"vpn.example.com", "sub.example.com", "", false, []string{"vpn.example.com", "sub.example.com"}},
		{"77.238.243.225", "sub.example.com", "shortlived", true, []string{"77.238.243.225"}},
	} {
		req, noCN := order(tc.id, tc.sub)
		if req.Profile != tc.profile || noCN != tc.noCN || len(req.Domains) != len(tc.names) || !req.Bundle {
			t.Errorf("%s: %+v, no CN %v", tc.id, req, noCN)
			continue
		}
		for i, name := range tc.names {
			if req.Domains[i] != name {
				t.Errorf("%s: domain %d %q, want %q", tc.id, i, req.Domains[i], name)
			}
		}
	}
}
