package singbox

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"mikan/internal/sub/model"
	"mikan/internal/sub/routing"
	"mikan/internal/sub/subtest"
	"mikan/internal/sub/subtest/corecheck"
)

// A resolver given as a bare IPv6 address is that address, with its port when it has one,
// and sing-box takes the config.
func TestDNSIPv6Resolver(t *testing.T) {
	lists(t)
	prof := profileOf(subtest.TwoNodes())
	prof.Routes = routing.Routes{DNS: routing.DNS{
		Nameserver:  []string{"2606:4700:4700::1111"},
		ProxyServer: []string{"[2606:4700:4700::1001]:5353"},
		Policy:      []routing.DNSPolicy{{Match: "+.cn", Servers: []string{"2400:3200::1#" + model.AliasGroup}}},
	}}
	raw, err := Render(context.Background(), prof, model.GroupNames{}, routing.All, model.Template{})
	if err != nil {
		t.Fatal(err)
	}
	var c sbConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	servers, _ := json.Marshal(c.DNS.Servers)
	if string(servers) != `[{"server":"2606:4700:4700::1111","tag":"remote","type":"udp"},`+
		`{"server":"2606:4700:4700::1001","server_port":5353,"tag":"local","type":"udp"},`+
		`{"detour":"`+model.AliasGroup+`","server":"2400:3200::1","tag":"policy-0","type":"udp"}]` {
		t.Fatalf("servers: %s", servers)
	}
	t.Run("sing-box", func(t *testing.T) { corecheck.SingBox(t, raw) })
}

// A key of the policy that lists names with commas is one DNS rule of all of them: rule sets
// of the categories, domains with their subdomains or not; what sing-box has no form of is
// left out, and a key left with nothing makes no rule.
func TestDNSPolicyKeys(t *testing.T) {
	lists(t)
	prof := profileOf(subtest.TwoNodes())
	prof.Routes = routing.Routes{DNS: routing.DNS{
		Nameserver: []string{"1.1.1.1"},
		Policy: []routing.DNSPolicy{
			{Match: "geosite:cn,category-ru", Servers: []string{"223.5.5.5"}},
			{Match: "+.a.com,+.b.com", Servers: []string{"77.88.8.8"}},
			{Match: "a.com, +.c.com,*.d.com", Servers: []string{"77.88.8.1"}},
			{Match: "+.e.com,*.f.com", Servers: []string{"1.0.0.1"}},
			{Match: "geosite:!cn", Servers: []string{"8.8.8.8"}},
			{Match: "geosite:category-ai-!cn,+.x.com", Servers: []string{"8.8.4.4"}},
			{Match: "rule-set:mikan-ru-core", Servers: []string{"9.9.9.9"}},
		},
	}}
	raw, err := Render(context.Background(), prof, model.GroupNames{}, routing.All, model.Template{})
	if err != nil {
		t.Fatal(err)
	}
	var c sbConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	rules, _ := json.Marshal(c.DNS.Rules)
	if string(rules) != `[{"domain_suffix":["a.com","b.com"],"server":"policy-0"},`+
		`{"domain_suffix":["e.com"],"server":"policy-1"},`+
		`{"domain":["a.com"],"domain_suffix":["c.com"],"server":"policy-2"},`+
		`{"rule_set":["geosite-category-ai-!cn"],"server":"policy-3"},`+
		`{"rule_set":["geosite-cn","geosite-category-ru"],"server":"policy-4"}]` {
		t.Fatalf("rules: %s", rules)
	}
	// The servers of the keys left with nothing are not there.
	servers := map[string]any{}
	for _, s := range c.DNS.Servers {
		servers[s["tag"].(string)] = s["server"]
	}
	want := map[string]any{"remote": "1.1.1.1", "local": "1.1.1.1", "policy-0": "77.88.8.8", "policy-1": "1.0.0.1", "policy-2": "77.88.8.1",
		"policy-3": "8.8.4.4", "policy-4": "223.5.5.5"}
	if len(servers) != len(want) {
		t.Errorf("servers: %v", c.DNS.Servers)
	}
	for tag, srv := range want {
		if servers[tag] != srv {
			t.Errorf("%s: %v, want %v", tag, servers[tag], srv)
		}
	}
	sets := map[string]string{}
	for _, s := range c.Route.RuleSet {
		sets[s["tag"].(string)], _ = s["url"].(string)
	}
	for _, name := range []string{"cn", "category-ru", "category-ai-!cn"} {
		if url := sets["geosite-"+name]; url != routing.MetaCubeXRuleSet("geosite", name) {
			t.Errorf("geosite-%s: %q", name, url)
		}
	}
	for tag, url := range sets {
		if strings.ContainsAny(tag, ",+") || strings.Contains(url, "%2C") {
			t.Errorf("a rule set of a key, not of a category: %s %s", tag, url)
		}
	}
	t.Run("sing-box", func(t *testing.T) { corecheck.SingBox(t, raw) })
}

// A geosite name is a category of MetaCubeX's only as their rule sets are named.
func TestCategory(t *testing.T) {
	for name, want := range map[string]bool{"cn": true, "category-ai-!cn": true, "apple@cn": true, "geolocation-!cn": true,
		"!cn": false, "+.a.com": false, "a.com": false, "cn,ru": false, "CN": false} {
		if category(name) != want {
			t.Errorf("%q: %v", name, !want)
		}
	}
}
