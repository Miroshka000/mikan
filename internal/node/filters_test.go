package node

import (
	"encoding/json"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/hub/executor"

	"mikan/internal/nodeapi"
	"mikan/internal/proto"
)

func TestEgressRules(t *testing.T) {
	st := nodeapi.DesiredState{
		Filters: &nodeapi.Filters{Egress: nodeapi.Egress{
			// "+2525" and "-1" pass strconv.Atoi, yet mihomo refuses the whole config over
			// them; " 1194 " is still a port.
			Ports:    []string{"465", "1000-2000", "0", "70000", "9-8", "x", "587,REJECT", "+2525", "-1", "1-+5", " 1194 "},
			Networks: []string{"203.0.113.7/24", "198.51.100.9", "2001:db8::1/32", "nope", "::ffff:192.0.2.0/120", "fe80::1%eth0"},
			Domains:  []string{"*.Mail.Example", ".spam.example", "bad,REJECT", "", "1.2.3.4", "bad)", "ex ample.com"},
		}},
		Warp: &nodeapi.Warp{Domains: []string{"openai.com"}},
	}
	got := strings.Join(rules(st, true), "\n")
	want := strings.Join([]string{
		"DST-PORT,25,REJECT",
		"DST-PORT,465,REJECT",
		"DST-PORT,1000-2000,REJECT",
		"DST-PORT,1194,REJECT",
		"IP-CIDR,203.0.113.0/24,REJECT",
		"IP-CIDR,198.51.100.9/32,REJECT",
		"IP-CIDR6,2001:db8::/32,REJECT",
		"IP-CIDR,192.0.2.0/24,REJECT",
		"DOMAIN-SUFFIX,mail.example,REJECT",
		"DOMAIN-SUFFIX,spam.example,REJECT",
		"DOMAIN-SUFFIX,openai.com,WARP",
		"MATCH,DIRECT",
	}, "\n")
	if got != want {
		t.Fatalf("rules:\n%s\nwant:\n%s", got, want)
	}
	// The rules are part of what swaps the tunnel's routes.
	if routesKey(st, true) == routesKey(nodeapi.DesiredState{Warp: st.Warp}, true) {
		t.Fatal("the egress filter does not change the routes key")
	}
	if strings.Join(rules(nodeapi.DesiredState{}, true), "\n") != "DST-PORT,25,REJECT\nMATCH,DIRECT" {
		t.Fatal("no filters, yet rules changed")
	}
}

// mihomo's own parser takes a config with every kind of filter entry, the junk the node
// leaves out included: one rule it cannot parse would cost the node the whole state.
func TestFiltersConfigParses(t *testing.T) {
	for _, f := range []*nodeapi.Filters{
		nil,
		{},
		{
			Egress: nodeapi.Egress{
				Ports:    []string{"465", "587", "2525", "6881-6889", "+25", "-1", "65536", "1-+5", "x"},
				Networks: []string{"203.0.113.0/24", "198.51.100.7", "2001:db8::/32", "::ffff:192.0.2.0/120", "junk", "10.0.0.0/33"},
				Domains:  []string{"example.com", "*.mail.example", "пример.рф", "a,b", "bad)", "#x", "1.2.3.4"},
			},
			Ingress: nodeapi.Ingress{Allow: true, Networks: []string{"203.0.113.0/24", "junk"}},
		},
	} {
		st := cascadeState(t)
		st.Filters = f
		raw, _, err := buildConfig(st, proto.Cert{CertPath: "/tmp/c.pem", KeyPath: "/tmp/k.pem"}, false, "")
		if err != nil {
			t.Fatal(err)
		}
		var cfg struct {
			Rules []string `json:"rules"`
		}
		if err := json.Unmarshal(raw, &cfg); err != nil {
			t.Fatal(err)
		}
		parsed, err := executor.ParseWithBytes(raw)
		if err != nil {
			t.Fatalf("mihomo refuses the config with %+v: %v", f, err)
		}
		if len(parsed.Rules) != len(cfg.Rules) {
			t.Fatalf("parsed %d rules of %d", len(parsed.Rules), len(cfg.Rules))
		}
		if f == nil || len(f.Egress.Ports) == 0 {
			continue
		}
		got := strings.Join(cfg.Rules, "\n")
		// Only what parses, after the node's own REJECT rules, before the cascade and WARP.
		at := strings.Index(got, "DST-PORT,6881-6889,REJECT")
		if at < 0 || at < strings.Index(got, "IP-CIDR,127.0.0.0/8,REJECT") || at > strings.Index(got, "IN-NAME,") || at > strings.Index(got, ",WARP") {
			t.Fatalf("order:\n%s", got)
		}
		n := 0
		for _, r := range cfg.Rules {
			if strings.HasSuffix(r, ",REJECT") {
				n++
			}
		}
		// The node's own: 11 private ranges and port 25; the filter's: 4 ports, 4
		// networks, 3 domains.
		if n != len(privateRules)+1+11 || !strings.Contains(got, "DOMAIN-SUFFIX,xn--e1afmkfd.xn--p1ai,REJECT") {
			t.Fatalf("REJECT rules: %d\n%s", n, got)
		}
	}
}

func TestIngress(t *testing.T) {
	r := NewRegistry("e", 0, time.Minute, time.Now, &fakeCore{})
	in, out := netip.MustParseAddr("203.0.113.5"), netip.MustParseAddr("198.51.100.5")
	mapped := netip.MustParseAddr("::ffff:203.0.113.5")
	if !r.admitFrom(in) || !r.admitFrom(out) {
		t.Fatal("no filter, yet refused")
	}

	r.SetIngress(&nodeapi.Filters{Ingress: nodeapi.Ingress{Networks: []string{"203.0.113.0/24", "junk"}}})
	if r.admitFrom(in) || r.admitFrom(mapped) || !r.admitFrom(out) {
		t.Fatal("the deny list")
	}

	r.SetIngress(&nodeapi.Filters{Ingress: nodeapi.Ingress{Allow: true, Networks: []string{"203.0.113.0/24"}}})
	if !r.admitFrom(in) || !r.admitFrom(mapped) || r.admitFrom(out) {
		t.Fatal("the allow list")
	}

	// An allow list where nothing parses would shut everyone out: it is no filter.
	r.SetIngress(&nodeapi.Filters{Ingress: nodeapi.Ingress{Allow: true, Networks: []string{"junk"}}})
	if !r.admitFrom(out) {
		t.Fatal("an empty allow list shut users out")
	}
	r.SetIngress(nil)
	if !r.admitFrom(in) {
		t.Fatal("off, yet refused")
	}
}

// The networks become ranges that do not overlap: nested, overlapping and touching
// networks, IPv4 beside IPv6, the edges of each range.
func TestIngressRanges(t *testing.T) {
	r := NewRegistry("e", 0, time.Minute, time.Now, &fakeCore{})
	r.SetIngress(&nodeapi.Filters{Ingress: nodeapi.Ingress{Allow: true, Networks: []string{
		"10.0.0.0/8", "10.1.0.0/16", // nested
		"192.0.2.0/25", "192.0.2.64/26", "192.0.2.128/25", // overlapping, touching
		"198.51.100.7",
		"::ffff:203.0.113.0/120", // IPv4 written as IPv6
		"2001:db8::/32",
		"0.0.0.0/32",
		"255.255.255.255",
		"ffff:ffff:ffff:ffff:ffff:ffff:ffff:fff0/124",
	}}})
	for addr, want := range map[string]bool{
		"10.0.0.0": true, "10.255.255.255": true, "9.255.255.255": false, "11.0.0.0": false, "10.1.2.3": true,
		"192.0.2.0": true, "192.0.2.127": true, "192.0.2.128": true, "192.0.2.255": true, "192.0.3.0": false, "192.0.1.255": false,
		"198.51.100.7": true, "198.51.100.6": false, "198.51.100.8": false,
		"203.0.113.9": true, "::ffff:203.0.113.9": true, "203.0.114.0": false,
		"2001:db8::1": true, "2001:db8:ffff:ffff:ffff:ffff:ffff:ffff": true, "2001:db9::": false, "2001:db7:ffff::": false,
		"::": false, "::a00:1": false, // IPv6, not 10.0.0.1
		"0.0.0.0": true, "0.0.0.1": false, "255.255.255.255": true, "255.255.255.254": false,
		"ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff": true, "ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffef": false,
	} {
		if got := r.admitFrom(mustAddr(addr)); got != want {
			t.Errorf("%s: admitted %v, want %v", addr, got, want)
		}
	}
	if in := r.ingress.Load(); len(in.ranges) != 9 {
		t.Errorf("ranges: %v", in.ranges)
	}
	r.SetIngress(&nodeapi.Filters{Ingress: nodeapi.Ingress{Networks: []string{"10.0.0.0/8"}}})
	if r.admitFrom(mustAddr("10.2.3.4")) || !r.admitFrom(mustAddr("11.2.3.4")) || !r.admitFrom(netip.Addr{}) {
		t.Fatal("the deny list")
	}
}

// The ingress filter turns a connection away before its user is looked at; another node
// of the panel relaying its users is never turned away.
func TestIngressInTunnel(t *testing.T) {
	inner := &recordingTunnel{}
	reg := NewRegistry("e", 0, time.Minute, time.Now, &fakeCore{})
	reg.SetIngress(&nodeapi.Filters{Ingress: nodeapi.Ingress{Networks: []string{"198.51.100.0/24"}}})
	tun := &Tunnel{inner: inner, reg: reg}
	a, b := net.Pipe()
	defer b.Close()
	tun.HandleTCPConn(a, &C.Metadata{Type: C.VLESS, InName: nodeapi.RelayListener, InUser: "relay-2", SrcIP: mustAddr("198.51.100.20")})
	if inner.tcp.Load() != 1 {
		t.Fatal("the relay was turned away")
	}
	c, d := net.Pipe()
	defer d.Close()
	tun.HandleTCPConn(c, &C.Metadata{Type: C.VLESS, InName: "in-vless", InUser: "u1", SrcIP: mustAddr("198.51.100.21")})
	if inner.tcp.Load() != 1 {
		t.Fatal("a connection from a denied network reached mihomo")
	}
	if _, err := d.Write([]byte("x")); err == nil {
		t.Fatal("the denied connection is still open")
	}
}

func TestScopedEgressRules(t *testing.T) {
	st := cascadeState(t)
	st.Slots = []nodeapi.Slot{
		{Name: "s1", UUID: "A83DE115-CFED-416D-9F72-B0B6463A7C0F"},
		{Name: "s2", UUID: "49fdcd9f-61ed-49b4-b347-69e0ea4140a2"},
	}
	st.Filters = &nodeapi.Filters{Egress: nodeapi.Egress{
		Domains: []string{"example.com", "always.example"},
		Scoped: []nodeapi.ScopedEgress{
			{Kind: "port", Value: "1000-2000", Scope: "only", Slots: []string{"s1"}},
			{Kind: "network", Value: "203.0.113.0/24", Scope: "except", Slots: []string{"s1"}},
			{Kind: "domain", Value: "scoped.example", Scope: "except", Slots: []string{"s1"}},
			{Kind: "domain", Value: "open.example.com", Scope: "allow", Slots: []string{"s1"}},
		},
	}}
	raw, _, err := buildConfig(st, proto.Cert{}, false, "")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := executor.ParseWithBytes(raw)
	if err != nil {
		t.Fatal(err)
	}

	tuic := strings.ToLower(st.Slots[0].UUID)
	for _, tc := range []struct {
		name, inbound, user, host, ip string
		port                          uint16
		reject                        bool
	}{
		{name: "only selected slot", user: "s1", port: 1001, reject: true},
		{name: "only selected TUIC slot", user: tuic, port: 1001, reject: true},
		{name: "unselected slot", user: "s2", port: 1001},
		{name: "network exception", user: "s1", ip: "203.0.113.20"},
		{name: "network deny", user: "s2", ip: "203.0.113.20", reject: true},
		{name: "network deny on shared protocol", ip: "203.0.113.20", reject: true},
		{name: "scoped domain exception", user: "s1", host: "scoped.example"},
		{name: "scoped domain deny", user: "s2", host: "scoped.example", reject: true},
		{name: "scoped domain relay", inbound: nodeapi.RelayListener, user: "relay-2", host: "scoped.example"},
		{name: "personal exception to parent domain", user: "s1", host: "cdn.open.example.com"},
		{name: "personal exception in TUIC", user: tuic, host: "open.example.com"},
		{name: "same domain denied for another client", user: "s2", host: "open.example.com", reject: true},
		{name: "exception survives the relay", inbound: nodeapi.RelayListener, user: "relay-2", host: "cdn.open.example.com"},
		{name: "unrelated global rule still denies relay", inbound: nodeapi.RelayListener, user: "relay-2", host: "always.example", reject: true},
		{name: "mail ban stays above exception", user: "s1", host: "open.example.com", port: 25, reject: true},
		{name: "private network ban stays above exception", user: "s1", host: "open.example.com", ip: "127.0.0.1", reject: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &C.Metadata{InName: tc.inbound, InUser: tc.user, Host: tc.host, DstPort: tc.port}
			if tc.ip != "" {
				m.DstIP = netip.MustParseAddr(tc.ip)
			}
			for _, rule := range cfg.Rules {
				if matched, target := rule.Match(m, C.RuleMatchHelper{}); matched {
					if (target == "REJECT") != tc.reject {
						t.Fatalf("matched %s, reject wanted %v", target, tc.reject)
					}
					return
				}
			}
			t.Fatal("no route matched")
		})
	}
}
