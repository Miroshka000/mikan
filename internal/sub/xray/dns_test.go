package xray

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"mikan/internal/sub/model"
	"mikan/internal/sub/routing"
	"mikan/internal/sub/subtest"
)

// The names a key of the admin's nameserver-policy covers in xray's DNS, as they are now:
// one key may list several categories, lists or domains with commas, and what xray has no
// form of, or a category the geodata lacks, is left out of it.
func TestPolicyDomains(t *testing.T) {
	for _, c := range []struct {
		key  string
		geo  routing.Geodata
		want []string
	}{
		// geosite: categories in lower case, the ones the geodata has.
		{"geosite:cn", routing.Common, []string{"geosite:cn"}},
		{"geosite:CN", routing.Common, []string{"geosite:cn"}},
		{"geosite:category-ai-!cn", routing.Common, []string{"geosite:category-ai-!cn"}},
		{"geosite:cn,category-ru", routing.Common, []string{"geosite:cn", "geosite:category-ru"}},
		{"geosite: cn , category-ru ", routing.Common, []string{"geosite:cn", "geosite:category-ru"}},
		{"geosite:cn,no-such-category", routing.Common, []string{"geosite:cn"}},
		{"geosite:no-such-category", routing.Common, nil},
		{"geosite:ru-blocked", routing.Common, nil}, // Loyalsoldier's geodata lacks it
		{"geosite:ru-blocked", routing.Runetfreedom, []string{"geosite:ru-blocked"}},
		{"geosite:!cn", routing.Common, nil},                           // mihomo's negation is no category
		{"geosite:cn,+.a.com", routing.Common, []string{"geosite:cn"}}, // a domain after it is a category name
		{"geosite:", routing.Common, nil},
		{"GEOSITE:cn", routing.Common, nil}, // the prefix only in lower case
		// rule-set: the panel's lists as their nearest categories, none for others.
		{"rule-set:mikan-ru-sites", routing.Common, []string{"geosite:category-ru"}},
		{"rule-set:mikan-ru-sites", routing.Runetfreedom, []string{"geosite:category-ru", "geosite:ru-available-only-inside"}},
		{"rule-set:mikan-ru-core,mikan-games", routing.Common, []string{"geosite:category-ru", "geosite:category-games"}},
		{"rule-set: mikan-ru-core , mikan-gemini", routing.Common, []string{"geosite:category-ru", "geosite:google-gemini"}},
		{"rule-set:mikan-blocked", routing.Common, nil},
		{"rule-set:mikan-blocked", routing.Runetfreedom, []string{"geosite:ru-blocked"}},
		{"rule-set:mikan-telegram-ip", routing.Common, nil}, // only IPs
		{"rule-set:wl.yaml", routing.Common, nil},
		// Domains: "+." with every subdomain, "*." one label deep, the others as they are.
		{"+.example.com", routing.Common, []string{"domain:example.com"}},
		{"*.example.com", routing.Common, []string{`regexp:^[^.]+\.example\.com$`}},
		{"example.com", routing.Common, []string{"full:example.com"}},
		{"+.a.com,+.b.com", routing.Common, []string{"domain:a.com", "domain:b.com"}},
		{"a.com, +.b.com ,*.c.com", routing.Common, []string{"full:a.com", "domain:b.com", `regexp:^[^.]+\.c\.com$`}},
		{"+.a.com,geosite:cn", routing.Common, []string{"domain:a.com"}}, // a prefix only at the start
		{"a.com,,b.com", routing.Common, []string{"full:a.com", "full:b.com"}},
		{"full:example.com", routing.Common, nil},
		{"domain:example.com", routing.Common, nil},
		{"a.*.com", routing.Common, nil},
		{"*", routing.Common, nil},
		{"", routing.Common, nil},
		// A prefix with no name after it still makes a name.
		{"+.", routing.Common, []string{"domain:"}},
		{"*.", routing.Common, []string{`regexp:^[^.]+\.$`}},
	} {
		if got := policyDomains(c.key, c.geo); !slices.Equal(got, c.want) {
			t.Errorf("%q: %q, want %q", c.key, got, c.want)
		}
	}
}

// Each key of the policy that covers names makes an xray DNS server of each of its
// servers, for all of the key's names at once; a key that covers none makes no server.
func TestDNSPolicyServers(t *testing.T) {
	r := routing.Resolvers{
		Nameserver: []string{"https://1.1.1.1/dns-query"},
		Policy: []routing.DNSPolicy{
			{Match: "geosite:cn,category-ru", Servers: []string{"223.5.5.5"}},
			{Match: "+.a.com,+.b.com", Servers: []string{"77.88.8.8", "tcp://77.88.8.1"}},
			{Match: "rule-set:mikan-ru-core", Servers: []string{"77.88.8.8"}},
			{Match: "geosite:!cn", Servers: []string{"8.8.8.8"}},
			{Match: "geosite:no-such-category", Servers: []string{"8.8.8.8"}},
			{Match: "rule-set:wl.yaml", Servers: []string{"8.8.8.8"}},
		},
	}
	via := func(string) (Ref, bool) { return Ref{}, false }
	dns, rules := dnsOf(r, routing.Common, via, Ref{Tag: "proxy"})
	servers, _ := json.Marshal(dns["servers"])
	if string(servers) != `[{"address":"223.5.5.5","domains":["geosite:cn","geosite:category-ru"],"skipFallback":true},`+
		`{"address":"77.88.8.8","domains":["domain:a.com","domain:b.com"],"skipFallback":true},`+
		`{"address":"tcp://77.88.8.1:53","domains":["domain:a.com","domain:b.com"],"skipFallback":true},`+
		`{"address":"77.88.8.8","domains":["geosite:category-ru"],"skipFallback":true},`+
		`"https://1.1.1.1/dns-query"]` {
		t.Fatalf("servers: %s", servers)
	}
	// The servers of the keys that cover nothing are not asked, so they are not routed.
	got, _ := json.Marshal(rules)
	if string(got) != `[{"inboundTag":["dns"],"ip":["223.5.5.5","77.88.8.8","77.88.8.1","1.1.1.1"],"outboundTag":"direct"},{"inboundTag":["dns"],"outboundTag":"proxy"}]` {
		t.Fatalf("rules: %s", got)
	}
}

// A resolver given as a bare IPv6 address is that address, with its port when it has one,
// and xray takes the config; the DNS routing rules name its IP, past the tunnel or through
// the group after "#".
func TestDNSIPv6Resolver(t *testing.T) {
	prof := profileOf(subtest.TwoNodes())
	prof.Routes = routing.Routes{DNS: routing.DNS{
		Nameserver: []string{"2606:4700:4700::1111", "[2606:4700:4700::1001]:5353", "tcp://[2606:4700:4700::1111]", "2001:4860:4860::8888#" + model.AliasGroup},
		Policy:     []routing.DNSPolicy{{Match: "+.cn", Servers: []string{"2400:3200::1"}}},
	}}
	cs := render(t, prof, routing.All, model.Template{})
	servers, _ := json.Marshal(cs[0].DNS.Servers)
	if string(servers) != `[{"address":"2400:3200::1","domains":["domain:cn"],"skipFallback":true},`+
		`"2606:4700:4700::1111",{"address":"2606:4700:4700::1001","port":5353},"tcp://[2606:4700:4700::1111]:53","2001:4860:4860::8888"]` {
		t.Fatalf("servers: %s", servers)
	}
	var dns []string
	for _, r := range cs[0].Routing.Rules {
		if tags, _ := r["inboundTag"].([]any); len(tags) == 1 && tags[0] == "dns" {
			b, _ := json.Marshal(r)
			dns = append(dns, string(b))
		}
	}
	if !slices.Equal(dns, []string{
		`{"inboundTag":["dns"],"ip":["2400:3200::1","2606:4700:4700::1111","2606:4700:4700::1001"],"outboundTag":"direct"}`,
		`{"balancerTag":"Авто","inboundTag":["dns"],"ip":["2001:4860:4860::8888"]}`,
		`{"balancerTag":"Авто","inboundTag":["dns"]}`,
	}) {
		t.Fatalf("DNS rules:\n%s", strings.Join(dns, "\n"))
	}
}
