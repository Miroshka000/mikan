package routing

import (
	"strconv"
	"testing"
)

func nodeTarget(id int64) string { return targetNode + strconv.FormatInt(id, 10) }

func TestRoutesCheck(t *testing.T) {
	exists := func(id int64) bool { return id == 2 }
	good := Routes{Services: map[string]string{"youtube": TargetVPN, "ai": nodeTarget(2)}, Direct: []string{"ru"},
		DNS: DNS{Nameserver: []string{"https://1.1.1.1/dns-query#VPN", "8.8.8.8", "1.1.1.1:53", "system", "dhcp://en0"}, Policy: []DNSPolicy{{Match: "geosite:cn", Servers: []string{"tls://223.5.5.5"}}}}}
	if err := good.Check(exists); err != nil {
		t.Fatal(err)
	}
	for code, r := range map[string]Routes{
		"routes_service":    {Services: map[string]string{"nope": TargetVPN}},
		"routes_target":     {Services: map[string]string{"youtube": nodeTarget(9)}},
		"routes_direct":     {Direct: []string{"nope"}},
		"routes_dns":        {DNS: DNS{Nameserver: []string{"ftp://x"}}},
		"routes_dns_policy": {DNS: DNS{Policy: []DNSPolicy{{Match: "geosite:cn,+.a.com", Servers: []string{"8.8.8.8"}}}}},
	} {
		if err := r.Check(exists); err == nil || err.Error() != code {
			t.Errorf("%s: %v", code, err)
		}
	}
	if err := (Routes{DNS: DNS{Policy: []DNSPolicy{{Match: "+.cn"}}}}).Check(exists); err == nil {
		t.Error("a policy without servers")
	}
}

func TestOwnListsCheck(t *testing.T) {
	exists := func(id int64) bool { return id == 2 }
	ok := RouteList{Name: "wl", URL: "https://example.com/wl.yaml", Behavior: "classical", Format: "yaml", Target: TargetVPN}
	if err := (Routes{Lists: []RouteList{ok}}).Check(exists); err != nil {
		t.Fatal(err)
	}
	with := func(f func(*RouteList)) Routes {
		l := ok
		f(&l)
		return Routes{Lists: []RouteList{l}}
	}
	for code, r := range map[string]Routes{
		"routes_list_name":     with(func(l *RouteList) { l.Name = "Bad Name" }),
		"routes_list_url":      with(func(l *RouteList) { l.URL = "http://example.com/x" }),
		"routes_list_behavior": with(func(l *RouteList) { l.Behavior = "nope" }),
		"routes_list_format":   with(func(l *RouteList) { l.Format = "mrs" }), // classical has no .mrs
		"routes_target":        with(func(l *RouteList) { l.Target = nodeTarget(9) }),
		"routes_servers":       {Servers: Servers{Interval: 5}},
	} {
		if err := r.Check(exists); err == nil || err.Error() != code {
			t.Errorf("%s: %v", code, err)
		}
	}
	if err := (Routes{Lists: []RouteList{ok, ok}}).Check(exists); err == nil || err.Error() != "routes_list_name" {
		t.Errorf("a name twice: %v", err)
	}
}

func TestParseMode(t *testing.T) {
	for in, want := range map[string]Mode{"": RUDirect, "all": All, "ru_direct": RUDirect, "blocked": Blocked, "nope": RUDirect} {
		if got := ParseMode(in); got != want {
			t.Errorf("ParseMode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNodeOf(t *testing.T) {
	// 0: no node.
	for target, want := range map[string]int64{nodeTarget(7): 7, "node:0": 0, "node:x": 0, TargetVPN: 0, "node:-1": 0} {
		got, ok := NodeOf(target)
		if ok != (want > 0) || ok && got != want {
			t.Errorf("NodeOf(%q) = %d, %v", target, got, ok)
		}
	}
}
