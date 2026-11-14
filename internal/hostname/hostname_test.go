package hostname

import (
	"os"
	"strings"
	"testing"
)

// The cases the installer checks too (installer/src/net.rs, names_match_the_panel).
func TestSharedCases(t *testing.T) {
	raw, err := os.ReadFile("testdata/hosts.txt")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimRight(line, "\r ")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		verdict, host, ok := strings.Cut(line, " ")
		if !ok || verdict != "name" && verdict != "ip" && verdict != "bad" {
			t.Fatalf("bad line %q", line)
		}
		if got := Name(host); got != (verdict == "name") {
			t.Errorf("Name(%q) = %v, the file says %s", host, got, verdict)
		}
		if got := Valid(host); got != (verdict != "bad") {
			t.Errorf("Valid(%q) = %v, the file says %s", host, got, verdict)
		}
		n++
	}
	if n < 20 {
		t.Fatalf("only %d cases", n)
	}
}

func TestLength(t *testing.T) {
	labels := strings.Repeat(strings.Repeat("a", 63)+".", 3)
	for _, c := range []struct {
		host string
		want bool
	}{
		{labels + strings.Repeat("b", 61), true},  // 253
		{labels + strings.Repeat("b", 62), false}, // 254
		{"", false},
		{".", false},
	} {
		if got := Name(c.host); got != c.want {
			t.Errorf("Name(%d characters) = %v, want %v", len(c.host), got, c.want)
		}
	}
}

// Special-use zones are valid names that never get a public certificate.
func TestReserved(t *testing.T) {
	for name, want := range map[string]bool{"node.test": true, "a.b.EXAMPLE": true, "router.lan": true, "nas.home.arpa": true, "localhost": true,
		"vpn.example.com": false, "contest.ru": false, "latest.io": false} {
		if Reserved(name) != want {
			t.Errorf("Reserved(%q) = %v", name, !want)
		}
	}
}

// A CA certifies public addresses and names outside the special-use zones, nothing else.
func TestPublic(t *testing.T) {
	for s, want := range map[string]bool{"vpn.example.com": true, "203.0.113.7": true, "77.238.243.225": true, "2a01:4f8::1": true,
		"10.0.0.5": false, "100.64.1.2": false, "fd00::1": false, "node.test": false, "router.lan": false, "localhost": false, "mikan": false, "": false} {
		if Public(s) != want {
			t.Errorf("Public(%q) = %v", s, !want)
		}
	}
}
