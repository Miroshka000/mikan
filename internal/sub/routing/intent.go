package routing

import (
	"net/url"
	"slices"
	"strconv"
	"strings"

	"mikan/internal/sub/model"
)

// Intent is what the panel's routing settings ask of every profile, apart from any core:
// the admin's rules, a route per service, own list and set of direct apps (and the blocked
// lists in their mode), the mode's own rules, where the rest goes, and the DNS. Each core
// writes what its apps take of it (mihomo, singbox, xray) and the nearest form of the rest.
//
// In a profile the rules go: the hosts of the panel and the nodes (HostRules), Head, Own,
// the routes in order, Tail, and Final for what none of them takes (Sequence).
type Intent struct {
	Mode Mode
	// Own are the admin's own rules (ServedRules).
	Own     []Rule
	Routes  []Route
	DNS     DNS // the admin's resolvers, over the panel's own (Resolvers)
	Servers Servers
	Tune    Tune
}

// Route is a service, an own list, a set of direct apps or the blocked lists as the
// settings send them: each condition is a rule of its own to To, and Lists are what its
// RULE-SET conditions read. A target of one node (ToNode) makes a group named after the
// route (Icon and Name, NameEN on a panel in English).
type Route struct {
	Icon, Name, NameEN string
	Rules              []Match
	Lists              []Provider
	To                 Target
}

// NewIntent is the intent of the settings: the mode, the admin's rules (ServedRules) and
// the routes. A service, list or set it does not know is left out (Routes.Check refuses
// them on save).
func NewIntent(m Mode, own []Rule, r Routes) Intent {
	in := Intent{Mode: m, Own: own, DNS: r.DNS, Servers: r.Servers, Tune: r.Tune}
	for _, s := range Services {
		if t, ok := r.Services[s.ID]; ok {
			in.Routes = append(in.Routes, Route{Icon: s.Icon, Name: s.Name, NameEN: s.NameEN, Rules: s.Rules, Lists: s.Providers, To: ServiceTarget(t)})
		}
	}
	for _, l := range r.Lists {
		pr := Provider{Name: ListProvider(l.Name), Behavior: l.Behavior, Format: l.Format, URL: l.URL}
		if l.Format == "mrs" {
			pr.Since = MRSSince
		}
		in.Routes = append(in.Routes, Route{Icon: "📋", Name: l.Name, NameEN: l.Name,
			Rules: []Match{{Type: "RULE-SET", Value: pr.Name, NoResolve: l.Behavior == "ipcidr"}}, Lists: []Provider{pr}, To: ServiceTarget(l.Target)})
	}
	for _, id := range r.Direct {
		if set, ok := DirectSetByID(id); ok {
			in.Routes = append(in.Routes, Route{Rules: ruleSets(set.Providers), Lists: set.Providers, To: Target{Kind: ToDirect}})
		}
	}
	if m == Blocked {
		in.Routes = append(in.Routes, Route{Rules: ruleSets(BlockedLists), Lists: BlockedLists, To: Target{Kind: ToMain}})
	}
	return in
}

func ruleSets(ps []Provider) []Match {
	out := make([]Match, len(ps))
	for i, p := range ps {
		out[i] = Match{Type: "RULE-SET", Value: p.Name}
	}
	return out
}

// Plain is the intent for an app that takes no rule lists (Stash, an app we do not know):
// no routes, and the blocked mode, which is made of them, as RUDirect.
func (in Intent) Plain() Intent {
	in.Routes = nil
	if in.Mode == Blocked {
		in.Mode = RUDirect
	}
	return in
}

// Head are the rules ahead of the admin's: the LAN past the tunnel, and QUIC refused when
// Tune says so (UDP to port 443: browsers and YouTube fall back to TCP).
func (in Intent) Head() []Rule {
	out := []Rule{Direct(Match{Type: "GEOIP", Value: "LAN", NoResolve: true})}
	if in.Tune.BlockQUIC {
		out = append(out, Rule{When: []Match{{Type: "NETWORK", Value: "UDP"}, {Type: "DST-PORT", Value: "443"}}, To: Target{Kind: ToReject}})
	}
	return out
}

// Tail are the mode's own rules after the routes: in RUDirect the Russian sites and IPs go
// past the tunnel by the cores' geodata (geosite category-ru, GeoIP ru).
func (in Intent) Tail() []Rule {
	if in.Mode != RUDirect {
		return nil
	}
	return []Rule{Direct(Match{Type: "GEOSITE", Value: "category-ru"}), Direct(Match{Type: "GEOIP", Value: "ru"})}
}

// Final is where what no rule takes goes: past the tunnel in the blocked mode, the main
// group in the others.
func (in Intent) Final() Target {
	if in.Mode == Blocked {
		return Target{Kind: ToDirect}
	}
	return Target{Kind: ToMain}
}

// Sequence are the rules of a profile in their order: pre (HostRules of direct, Head, Own),
// routed (a rule per condition of each of routes, those of the intent the core takes, in
// order), tail (Tail), and final (Final) for what none of them takes. to is where a target
// of route i of routes (-1: of no route) goes: Groups.Resolve, or the target as it is.
func (in Intent) Sequence(direct []string, routes []Route, to func(t Target, route int) Target) (pre, routed, tail []Rule, final Target) {
	for _, part := range [][]Rule{HostRules(direct), in.Head(), in.Own} {
		for _, r := range part {
			pre = append(pre, Rule{When: r.When, To: to(r.To, -1)})
		}
	}
	for i, r := range routes {
		for _, m := range r.Rules {
			routed = append(routed, Rule{When: []Match{m}, To: to(r.To, i)})
		}
	}
	for _, r := range in.Tail() {
		tail = append(tail, Rule{When: r.When, To: to(r.To, -1)})
	}
	return pre, routed, tail, to(in.Final(), -1)
}

// Resolvers are the DNS servers of a profile, as mihomo writes them: an IP, a URL, "#<group>"
// after one to go through a group (ParseResolver reads them). Policy resolves the names its
// Match covers with other servers.
type Resolvers struct {
	Nameserver  []string
	Default     []string // plain IPs that resolve the names of the DoH servers
	ProxyServer []string // the proxies' own names; none: Nameserver
	Policy      []DNSPolicy
	// FakeIP answers with addresses of its own and resolves past the tunnel only what goes
	// direct; off, every name is resolved before the connection.
	FakeIP bool
	IPv6   bool
}

// defaultNameservers are the DNS-over-HTTPS servers a profile resolves with, and the ones
// that resolve the proxy servers' own names.
var defaultNameservers = []string{"https://1.1.1.1/dns-query", "https://dns.google/dns-query"}

// Resolvers are the panel's resolvers for the mode with the admin's (DNS) over them key by
// key. The node has no IPv6 on most VPS: with it on, apps first try IPv6 through the tunnel
// and wait for the node's "network unreachable" before falling back.
func (in Intent) Resolvers() Resolvers {
	r := Resolvers{Nameserver: slices.Clone(defaultNameservers), Default: []string{"1.1.1.1", "8.8.8.8"}, FakeIP: !in.Tune.RealIP}
	if in.Mode != All {
		// GEOIP,ru makes the app resolve every domain itself. DoH straight from Russia
		// stalls under TSPU throttling, so it goes through the tunnel (the alias group has
		// a fixed name: "&" or "=" in a renamed group would break the "#group" suffix).
		// Russian domains resolve with Yandex DNS directly and keep working without the VPN.
		r.Nameserver = []string{"https://1.1.1.1/dns-query#" + model.AliasGroup, "https://8.8.8.8/dns-query#" + model.AliasGroup}
		r.ProxyServer = slices.Clone(defaultNameservers)
		r.Policy = []DNSPolicy{{Match: "geosite:category-ru", Servers: []string{"77.88.8.8", "77.88.8.1"}}}
	}
	d := in.DNS
	if len(d.Nameserver) > 0 {
		r.Nameserver = d.Nameserver
	}
	if len(d.Default) > 0 {
		r.Default = d.Default
	}
	if len(d.ProxyServer) > 0 {
		r.ProxyServer = d.ProxyServer
	}
	if len(d.Policy) > 0 {
		r.Policy = d.Policy
	}
	return r
}

// Resolver is a server of Resolvers in its parts.
type Resolver struct {
	// Scheme is udp, tcp, tls, https, h3 or quic; "system" is the device's own resolver.
	Scheme string
	Host   string
	Port   int    // 0: the scheme's own
	Path   string // of https and h3 when not /dns-query
	Via    string // the group it goes through; "": past the tunnel
}

// ParseResolver reads a server of Resolvers: "https://1.1.1.1/dns-query#VPN", "tls://dns.google",
// "77.88.8.8", "system". False for one no core but mihomo reads (dhcp://).
func ParseResolver(s string) (Resolver, bool) {
	s, via, _ := strings.Cut(s, "#")
	if s == "system" || s == "system://" {
		return Resolver{Scheme: "system"}, true
	}
	if !strings.Contains(s, "://") {
		s = "udp://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Hostname() == "" {
		return Resolver{}, false
	}
	switch u.Scheme {
	case "udp", "tcp", "tls", "quic", "https", "h3":
	default:
		return Resolver{}, false
	}
	r := Resolver{Scheme: u.Scheme, Host: u.Hostname(), Via: via}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return Resolver{}, false
		}
		r.Port = n
	}
	if (u.Scheme == "https" || u.Scheme == "h3") && u.Path != "" && u.Path != "/dns-query" {
		r.Path = u.Path
	}
	return r, true
}
