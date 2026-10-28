package singbox

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"mikan/internal/sub/model"
	"mikan/internal/sub/routing"
	"mikan/internal/sub/subtest"
	"mikan/internal/sub/subtest/corecheck"
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
	// The exit is a copy of the group over copies of its proxies, which dial through the entry;
	// the group itself and its proxies dial through nothing.
	if cascade := byTag["cascade"]; cascade["type"] != "selector" || !slices.Equal(cascade["outbounds"].([]string), []string{"chain1-hop2-exits"}) {
		t.Fatalf("a chain goes out of its exit: %v", cascade)
	}
	if exits := byTag["chain1-hop2-exits"]; exits["type"] != "urltest" || !slices.Equal(exits["outbounds"].([]string), []string{"chain1-hop2-b", "chain1-hop2-c"}) ||
		!slices.Equal(byTag["exits"]["outbounds"].([]string), []string{"b", "c"}) {
		t.Fatalf("the exit's copy: %v %v", exits, byTag["exits"])
	}
	if want := []hopCopy{{"chain1-hop2-", "b", "entry"}, {"chain1-hop2-", "c", "entry"}}; !slices.Equal(c.copies, want) {
		t.Fatalf("the exits dial through the entry: %+v", c.copies)
	}
	want := []model.Note{{Core: "singbox", Subject: "first", Code: "fallback_fastest"}, {Core: "singbox", Subject: "spread", Code: "balance_fastest"}}
	if !slices.Equal(c.notes, want) {
		t.Fatalf("notes: %+v", c.notes)
	}

	// Two chains through one proxy: each has its own copy, dialing through its own hop.
	two := convertGroups([]model.Group{
		{Name: "pick", Type: model.Select, Members: []string{"a"}},
		{Name: "one", Type: model.Chain, Members: []string{"entry", "a"}},
		{Name: "two", Type: model.Chain, Members: []string{"b", "a"}},
	}, tags)
	if want := []hopCopy{{"chain1-hop2-", "a", "entry"}, {"chain2-hop2-", "a", "b"}}; !slices.Equal(two.copies, want) || len(two.notes) != 0 {
		t.Fatalf("a proxy of two chains: %+v %+v", two.copies, two.notes)
	}
	if o := two.outbounds; !slices.Equal(o[0]["outbounds"].([]string), []string{"a"}) ||
		!slices.Equal(o[1]["outbounds"].([]string), []string{"chain1-hop2-a"}) || !slices.Equal(o[2]["outbounds"].([]string), []string{"chain2-hop2-a"}) {
		t.Fatalf("the chains go out of their copies, the select of the proxy: %v", o)
	}

	// A chain as a hop is entered by its first hop: entry, a, then b; the inner chain on its
	// own is a, then b.
	nested := convertGroups([]model.Group{
		{Name: "inner", Type: model.Chain, Members: []string{"a", "b"}},
		{Name: "outer", Type: model.Chain, Members: []string{"entry", "inner"}},
	}, tags)
	if want := []hopCopy{{"chain1-hop2-", "b", "a"}, {"chain2-hop2-", "a", "entry"}, {"chain2-hop3-", "b", "chain2-hop2-a"}}; !slices.Equal(nested.copies, want) ||
		!slices.Equal(nested.outbounds[1]["outbounds"].([]string), []string{"chain2-hop3-b"}) || len(nested.notes) != 0 {
		t.Fatalf("a nested chain: %+v %v %+v", nested.copies, nested.outbounds, nested.notes)
	}
}

// A chain of an own profile reaches the config: the exit's servers dial through the entry.
func TestChainInConfig(t *testing.T) {
	pl := plan{
		proxies: []model.Proxy{{Name: "entry", Type: "shadowsocks", Server: "203.0.113.1", Port: 8388, Cipher: "aes-128-gcm", Password: "x", UDP: true},
			{Name: "exit", Type: "trojan", Server: "198.51.100.2", Port: 443, Password: "y", TLS: &model.TLS{}, UDP: true}},
		groups: []model.Group{{Name: "cascade", Type: model.Chain, Members: []string{"entry", "exit"}}},
		rules:  []routing.Rule{{To: routing.Target{Kind: routing.ToGroup, Group: "cascade"}}},
		main:   "cascade",
	}
	cfg := config(pl, &ruleSets{ctx: t.Context()})
	raw, _ := json.Marshal(cfg["outbounds"])
	if !strings.Contains(string(raw), `"detour":"entry","password":"y"`) || !strings.Contains(string(raw), `"tag":"chain1-hop2-exit"`) ||
		strings.Count(string(raw), `"detour"`) != 1 || cfg["route"].(map[string]any)["final"] != "cascade" {
		t.Fatalf("outbounds: %s", raw)
	}
}

// Two chains share a proxy and a select holds it: each chain dials through its own entry,
// and the proxy in the select through nothing; a group as a hop is copied too, and a third
// hop dials through the second's copy. sing-box takes the config.
func TestChainsShareProxy(t *testing.T) {
	ss := func(name, ip string) model.Proxy {
		return model.Proxy{Name: name, Type: "shadowsocks", Server: ip, Port: 8388, Cipher: "aes-128-gcm", Password: "x", UDP: true}
	}
	pl := plan{
		proxies: []model.Proxy{ss("de", "203.0.113.1"), ss("nl", "203.0.113.2"), ss("exit", "198.51.100.2")},
		groups: []model.Group{
			{Name: "pick", Type: model.Select, Members: []string{"one", "two", "exit"}},
			{Name: "one", Type: model.Chain, Members: []string{"de", "exit"}},
			{Name: "two", Type: model.Chain, Members: []string{"nl", "exit"}},
			{Name: "auto", Type: model.URLTest, Members: []string{"exit", "nl"}},
			{Name: "three", Type: model.Chain, Members: []string{"de", "auto"}},
			{Name: "four", Type: model.Chain, Members: []string{"de", "nl", "exit"}},
		},
		rules: []routing.Rule{{To: routing.Target{Kind: routing.ToGroup, Group: "pick"}}},
		main:  "pick",
	}
	cfg := config(pl, &ruleSets{ctx: t.Context()})
	byTag := map[string]map[string]any{}
	for _, o := range cfg["outbounds"].([]map[string]any) {
		byTag[str(o["tag"])] = o
	}
	if byTag["exit"]["detour"] != nil || byTag["chain1-hop2-exit"]["detour"] != "de" || byTag["chain2-hop2-exit"]["detour"] != "nl" ||
		byTag["chain1-hop2-exit"]["server"] != "198.51.100.2" {
		t.Fatalf("outbounds: %v", cfg["outbounds"])
	}
	if !slices.Equal(byTag["one"]["outbounds"].([]string), []string{"chain1-hop2-exit"}) || !slices.Equal(byTag["two"]["outbounds"].([]string), []string{"chain2-hop2-exit"}) ||
		!slices.Equal(byTag["pick"]["outbounds"].([]string), []string{"one", "two", "exit"}) {
		t.Fatalf("groups: %v %v %v", byTag["one"], byTag["two"], byTag["pick"])
	}
	// A group as a hop is a copy of it over copies.
	if auto := byTag["chain3-hop2-auto"]; auto["type"] != "urltest" || !slices.Equal(auto["outbounds"].([]string), []string{"chain3-hop2-exit", "chain3-hop2-nl"}) ||
		byTag["chain3-hop2-nl"]["detour"] != "de" || byTag["nl"]["detour"] != nil || !slices.Equal(byTag["auto"]["outbounds"].([]string), []string{"exit", "nl"}) {
		t.Fatalf("a group as a hop: %v %v", auto, byTag["auto"])
	}
	// A third hop dials through the second's copy.
	if byTag["chain4-hop2-nl"]["detour"] != "de" || byTag["chain4-hop3-exit"]["detour"] != "chain4-hop2-nl" ||
		!slices.Equal(byTag["four"]["outbounds"].([]string), []string{"chain4-hop3-exit"}) {
		t.Fatalf("three hops: %v %v", byTag["chain4-hop3-exit"], byTag["four"])
	}
	raw, _ := json.Marshal(cfg)
	corecheck.SingBox(t, raw)
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
