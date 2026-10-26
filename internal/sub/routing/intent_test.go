package routing

import (
	"slices"
	"testing"
)

// The routes go in the order their rules apply: the services in the catalog's order, the
// own lists, the sets of direct apps, and the blocked lists in their mode.
func TestNewIntent(t *testing.T) {
	r := Routes{
		Services: map[string]string{"torrents": TargetDirect, "youtube": TargetVPN, "ai": nodeTarget(2), "ads": TargetBlock},
		Lists:    []RouteList{{Name: "ip", URL: "https://example.com/ip.mrs", Behavior: "ipcidr", Format: "mrs", Target: TargetBlock}},
		Direct:   []string{"ru", "nope"},
	}
	own := []Rule{Direct(Match{Type: "DOMAIN", Value: "a.example"})}
	in := NewIntent(Blocked, own, r)
	var names []string
	for _, x := range in.Routes {
		names = append(names, x.Name)
	}
	if !slices.Equal(names, []string{"Реклама и трекеры", "YouTube", "ChatGPT, Claude, Gemini", "Торренты", "ip", "", ""}) {
		t.Fatalf("routes: %q", names)
	}
	ads, ai, list, ru, blocked := in.Routes[0], in.Routes[2], in.Routes[4], in.Routes[5], in.Routes[6]
	if ads.To.Kind != ToReject || ai.To != (Target{Kind: ToNode, Node: 2}) || in.Routes[3].To.Kind != ToDirect || in.Routes[1].To.Kind != ToMain {
		t.Fatalf("targets: %+v", in.Routes)
	}
	if list.Rules[0] != (Match{Type: "RULE-SET", Value: "own-ip", NoResolve: true}) || list.Lists[0].Since != MRSSince || list.To.Kind != ToReject {
		t.Fatalf("own list: %+v", list)
	}
	if ru.To.Kind != ToDirect || len(ru.Rules) != len(DirectSets[0].Providers) || blocked.To.Kind != ToMain || blocked.Rules[0].Value != BlockedLists[0].Name {
		t.Fatalf("sets: %+v %+v", ru, blocked)
	}
	if len(in.Own) != 1 || in.Final().Kind != ToDirect || in.Tail() != nil {
		t.Fatalf("blocked: %+v", in)
	}

	plain := in.Plain()
	if plain.Routes != nil || plain.Mode != RUDirect || plain.Final().Kind != ToMain || len(plain.Tail()) != 2 || len(in.Routes) != 7 {
		t.Fatalf("an app without lists: %+v", plain)
	}
	if NewIntent(All, nil, r).Routes[len(r.Services)+len(r.Lists)].To.Kind != ToDirect {
		t.Fatal("no blocked lists out of their mode")
	}
}

// The LAN goes direct ahead of everything, QUIC is refused when the admin says so.
func TestIntentHead(t *testing.T) {
	head := Intent{Tune: Tune{BlockQUIC: true}}.Head()
	if len(head) != 2 || head[0].Clash("DIRECT") != "GEOIP,LAN,DIRECT,no-resolve" || head[1].Clash("REJECT") != "AND,((NETWORK,UDP),(DST-PORT,443)),REJECT" {
		t.Fatalf("head: %+v", head)
	}
	if len((Intent{}).Head()) != 1 {
		t.Fatal("QUIC refused unasked")
	}
}

// The panel's resolvers by the mode, the admin's over them key by key.
func TestResolvers(t *testing.T) {
	all := Intent{Mode: All}.Resolvers()
	if !slices.Equal(all.Nameserver, defaultNameservers) || all.ProxyServer != nil || all.Policy != nil || !all.FakeIP || all.IPv6 {
		t.Fatalf("all: %+v", all)
	}
	ru := Intent{Mode: RUDirect, Tune: Tune{RealIP: true}}.Resolvers()
	if ru.Nameserver[0] != "https://1.1.1.1/dns-query#PROXY" || !slices.Equal(ru.ProxyServer, defaultNameservers) || ru.Policy[0].Match != "geosite:category-ru" || ru.FakeIP {
		t.Fatalf("ru_direct: %+v", ru)
	}
	own := Intent{Mode: RUDirect, DNS: DNS{Nameserver: []string{"tls://dns.google"}, Policy: []DNSPolicy{{Match: "+.cn", Servers: []string{"223.5.5.5"}}}}}.Resolvers()
	if !slices.Equal(own.Nameserver, []string{"tls://dns.google"}) || !slices.Equal(own.ProxyServer, defaultNameservers) || len(own.Policy) != 1 || own.Policy[0].Match != "+.cn" {
		t.Fatalf("the admin's: %+v", own)
	}
}

func TestParseResolver(t *testing.T) {
	for in, want := range map[string]Resolver{
		"https://1.1.1.1/dns-query#PROXY": {Scheme: "https", Host: "1.1.1.1", Via: "PROXY"},
		"https://dns.example/q":           {Scheme: "https", Host: "dns.example", Path: "/q"},
		"77.88.8.8":                       {Scheme: "udp", Host: "77.88.8.8"},
		"1.1.1.1:5353":                    {Scheme: "udp", Host: "1.1.1.1", Port: 5353},
		"tls://dns.google":                {Scheme: "tls", Host: "dns.google"},
		"system":                          {Scheme: "system"},
	} {
		if got, ok := ParseResolver(in); !ok || got != want {
			t.Errorf("%s: %+v %v", in, got, ok)
		}
	}
	for _, bad := range []string{"dhcp://en0", "ftp://x", "https://", "1.1.1.1:99999"} {
		if _, ok := ParseResolver(bad); ok {
			t.Errorf("%s read", bad)
		}
	}
}

// A rule of an own profile reads with its target, a group by its name; a list's lines read
// without one.
func TestReadRule(t *testing.T) {
	for in, want := range map[string]Rule{
		"IP-CIDR,10.0.0.0/8,DIRECT,no-resolve":      {When: []Match{{Type: "IP-CIDR", Value: "10.0.0.0/8", NoResolve: true}}, To: Target{Kind: ToDirect}},
		"AND,((NETWORK,UDP),(DST-PORT,443)),REJECT": {When: []Match{{Type: "NETWORK", Value: "UDP"}, {Type: "DST-PORT", Value: "443"}}, To: Target{Kind: ToReject}},
		"RULE-SET,ru,🌍 Сеть":                        {When: []Match{{Type: "RULE-SET", Value: "ru"}}, To: Target{Kind: ToGroup, Group: "🌍 Сеть"}},
		"MATCH,REJECT-DROP":                         {To: Target{Kind: ToDrop}},
	} {
		got, ok := ReadRule(in)
		if !ok || !slices.Equal(got.When, want.When) || got.To != want.To {
			t.Errorf("%s: %+v %v", in, got, ok)
		}
	}
	for _, bad := range []string{"OR,((DOMAIN,a.com),(DOMAIN,b.com)),DIRECT", "NOT,((DOMAIN,a.com)),DIRECT", "DOMAIN,a.com", "AND,(NETWORK,UDP),DIRECT"} {
		if _, ok := ReadRule(bad); ok {
			t.Errorf("%s read", bad)
		}
	}
	if m, ok := ReadMatch("IP-CIDR,149.154.160.0/20,no-resolve"); !ok || m[0] != (Match{Type: "IP-CIDR", Value: "149.154.160.0/20", NoResolve: true}) {
		t.Fatalf("a list's line: %+v", m)
	}
}

func TestHostRules(t *testing.T) {
	var got []string
	for _, r := range HostRules([]string{"203.0.113.7", "vpn.example.com", "2001:db8::1", "", "vpn.example.com"}) {
		got = append(got, r.Clash("DIRECT"))
	}
	want := []string{"IP-CIDR,203.0.113.7/32,DIRECT,no-resolve", "DOMAIN,vpn.example.com,DIRECT", "IP-CIDR6,2001:db8::1/128,DIRECT,no-resolve"}
	if !slices.Equal(got, want) {
		t.Fatalf("%q", got)
	}
}
