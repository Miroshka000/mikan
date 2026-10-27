package singbox

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"mikan/internal/sub/model"
	"mikan/internal/sub/routing"
	"mikan/internal/sub/subtest"
)

// What sing-box has no group for becomes the nearest, and the preview is told: a fallback
// and a load-balance test their members as an urltest; a chain dials each hop through the
// one before it, and its name stays an outbound.
func TestConvertGroups(t *testing.T) {
	tags := map[string]bool{"a": true, "b": true, "c": true, "entry": true}
	c := convertGroups([]model.Group{
		{Name: "pick", Type: model.Select, Members: []string{"fast", "DIRECT", "REJECT", "gone", "a"}},
		{Name: "fast", Type: model.URLTest, Members: []string{"a", "b"}, URL: routing.ProbeURL, Interval: 120, Tolerance: 80},
		{Name: "first", Type: model.Fallback, Members: []string{"a", "b"}},
		{Name: "spread", Type: model.LoadBalance, Members: []string{"a", "b"}, Interval: 3600},
		{Name: "exits", Type: model.URLTest, Members: []string{"b", "c"}},
		{Name: "cascade", Type: model.Chain, Members: []string{"entry", "exits"}},
		{Name: "broken", Type: model.Chain, Members: []string{"entry", "nowhere"}},
		{Name: "closed", Type: model.Select, Members: []string{"REJECT"}},
	}, tags)
	byTag := map[string]map[string]any{}
	for _, o := range c.outbounds {
		byTag[o["tag"].(string)] = o
	}
	if byTag["broken"] != nil || byTag["closed"] != nil {
		t.Fatalf("a chain with a hop sing-box lacks, a group of nothing: %v", c.outbounds)
	}
	if pick := byTag["pick"]; !slices.Equal(pick["outbounds"].([]string), []string{"fast", "direct", "a"}) || pick["type"] != "selector" {
		t.Fatalf("select: %v", pick)
	}
	if fast := byTag["fast"]; fast["type"] != "urltest" || fast["interval"] != "120s" || fast["tolerance"] != 80 || fast["url"] != routing.ProbeURL {
		t.Fatalf("url-test: %v", fast)
	}
	if byTag["first"]["type"] != "urltest" || byTag["spread"]["type"] != "urltest" {
		t.Fatalf("the nearest: %v %v", byTag["first"], byTag["spread"])
	}
	// sing-box refuses an interval past the idle timeout, 30 minutes unless given.
	if fast, spread := byTag["fast"], byTag["spread"]; fast["idle_timeout"] != nil || spread["interval"] != "3600s" || spread["idle_timeout"] != "7200s" {
		t.Fatalf("idle timeout: %v %v", fast, spread)
	}
	if cascade := byTag["cascade"]; cascade["type"] != "selector" || !slices.Equal(cascade["outbounds"].([]string), []string{"exits"}) {
		t.Fatalf("a chain goes out of its exit: %v", cascade)
	}
	if c.detours["b"] != "entry" || c.detours["c"] != "entry" || len(c.detours) != 2 {
		t.Fatalf("the exits dial through the entry: %v", c.detours)
	}
	want := []model.Note{{Core: "singbox", Group: "first", Code: "fallback_fastest"}, {Core: "singbox", Group: "spread", Code: "balance_fastest"}}
	if !slices.Equal(c.notes, want) {
		t.Fatalf("notes: %+v", c.notes)
	}

	// A proxy one chain dials through a hop keeps it in another.
	two := convertGroups([]model.Group{
		{Name: "one", Type: model.Chain, Members: []string{"entry", "a"}},
		{Name: "two", Type: model.Chain, Members: []string{"b", "a"}},
	}, tags)
	if two.detours["a"] != "entry" || len(two.notes) != 1 || two.notes[0].Code != "chain_dialer_taken" {
		t.Fatalf("a taken dialer: %v %+v", two.detours, two.notes)
	}

	// A chain as a hop is entered by its first hop: entry, a, then b.
	nested := convertGroups([]model.Group{
		{Name: "inner", Type: model.Chain, Members: []string{"a", "b"}},
		{Name: "outer", Type: model.Chain, Members: []string{"entry", "inner"}},
	}, tags)
	if nested.detours["a"] != "entry" || nested.detours["b"] != "a" || len(nested.detours) != 2 || len(nested.notes) != 0 {
		t.Fatalf("a nested chain: %v %+v", nested.detours, nested.notes)
	}
}

// A chain of an own profile reaches the config: the exit's servers dial through the entry.
func TestChainInConfig(t *testing.T) {
	pl := plan{
		proxies: []map[string]any{{"name": "entry", "type": "ss", "server": "203.0.113.1", "port": 8388, "cipher": "aes-128-gcm", "password": "x"},
			{"name": "exit", "type": "trojan", "server": "198.51.100.2", "port": 443, "password": "y", "tls": true}},
		groups: []model.Group{{Name: "cascade", Type: model.Chain, Members: []string{"entry", "exit"}}},
		rules:  []routing.Rule{{To: routing.Target{Kind: routing.ToGroup, Group: "cascade"}}},
		main:   "cascade",
	}
	cfg := config(t.Context(), pl)
	raw, _ := json.Marshal(cfg["outbounds"])
	if !strings.Contains(string(raw), `"detour":"entry","password":"y"`) || cfg["route"].(map[string]any)["final"] != "cascade" {
		t.Fatalf("outbounds: %s", raw)
	}
}

// The conditions sing-box has: ports and ranges, the source, a dropped rule; what it would
// refuse is left out rather than failing the config.
func TestConditions(t *testing.T) {
	s := ruleSets{used: map[string]map[string]any{}}
	for m, want := range map[routing.Match]string{
		{Type: "DST-PORT", Value: "443/8000-9000"}:   `{"port":[443],"port_range":["8000:9000"]}`,
		{Type: "SRC-PORT", Value: "53"}:              `{"source_port":[53]}`,
		{Type: "SRC-IP-CIDR", Value: "10.0.0.0/8"}:   `{"source_ip_cidr":["10.0.0.0/8"]}`,
		{Type: "PROCESS-PATH", Value: "/usr/bin/x"}:  `{"process_path":["/usr/bin/x"]}`,
		{Type: "PROCESS-NAME", Value: "ru.sber.app"}: `{"package_name":["ru.sber.app"]}`,
		{Type: "GEOIP", Value: "LAN"}:                `{"ip_is_private":true}`,
	} {
		c, ok := s.condition(m)
		raw, _ := json.Marshal(c)
		if !ok || string(raw) != want {
			t.Errorf("%v: %s %v", m, raw, ok)
		}
	}
	for _, m := range []routing.Match{{Type: "IP-CIDR", Value: "not/cidr"}, {Type: "DOMAIN-REGEX", Value: "(?<=x)"}, {Type: "IP-ASN", Value: "13335"},
		{Type: "DOMAIN-WILDCARD", Value: "*.a.com"}, {Type: "DST-PORT", Value: "0"}, {Type: "RULE-SET", Value: "unknown"}} {
		if c, ok := s.condition(m); ok {
			t.Errorf("%v: %v", m, c)
		}
	}
	if a, _ := action(routing.Target{Kind: routing.ToDrop}, nil); a["method"] != "drop" {
		t.Errorf("REJECT-DROP drops: %v", a)
	}
}

// A DNS server named by a domain resolves its name with the default nameservers: without it
// sing-box refuses the config.
func TestDomainResolver(t *testing.T) {
	sets := ruleSets{used: map[string]map[string]any{}}
	dns, local := dnsOf(routing.Resolvers{Nameserver: []string{"tls://dns.google#VPN"}, Default: []string{"dhcp://en0", "9.9.9.9"}, ProxyServer: []string{"1.1.1.1"}},
		map[string]bool{"VPN": true}, &sets, "")
	raw, _ := json.Marshal(dns["servers"])
	if local != "local" || !strings.Contains(string(raw), `{"detour":"VPN","domain_resolver":"bootstrap","server":"dns.google","tag":"remote","type":"tls"}`) ||
		!strings.Contains(string(raw), `{"server":"9.9.9.9","tag":"bootstrap","type":"udp"}`) {
		t.Fatalf("servers: %s", raw)
	}
	plain, _ := dnsOf(routing.Resolvers{Nameserver: []string{"1.1.1.1"}}, nil, &sets, "")
	if raw, _ := json.Marshal(plain["servers"]); strings.Contains(string(raw), "bootstrap") {
		t.Fatalf("no domain, no bootstrap: %s", raw)
	}
}

// The sing-box apps' own balance goes over the common one; the Clash apps' leaves it be.
func TestSingBoxBalance(t *testing.T) {
	lists(t)
	prof := profileOf(subtest.TwoNodes())
	prof.Routes.Servers = routing.Servers{Interval: 120, SingBox: routing.SingBoxServers{Interval: 600, Tolerance: 80}, Mihomo: routing.MihomoServers{Type: "load-balance"}}
	c := renderSingBox(t, prof, routing.All, model.Template{})
	auto := outbound(c, "Авто")
	if auto["type"] != "urltest" || auto["interval"] != "600s" || auto["tolerance"] != float64(80) {
		t.Fatalf("auto: %v", auto)
	}
}
