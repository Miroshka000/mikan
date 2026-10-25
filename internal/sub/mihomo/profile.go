package mihomo

import (
	"net/netip"
	"slices"
	"strings"

	"mikan/internal/sub/apps"
	"mikan/internal/sub/model"
	"mikan/internal/sub/routing"
)

// Profile is what a mihomo profile is made of.
type Profile struct {
	Proxies []model.Proxy // the user's (build.Proxies)
	Nodes   []model.Node  // enabled nodes in display order
	Direct  []string      // hosts that bypass the tunnel: the panel and every node
	// Rules are the admin's own Clash rules (routing.ServedRules) that the app's core
	// parses (RulesFor), before the built-in routing.
	Rules []string
	// Routes go after the admin's rules, for the apps that take rule lists (App); Lang
	// names the groups they add.
	Routes routing.Routes
	App    apps.App
	Lang   string
}

// geoxURL is the geodata Koala Clash ships (MetaCubeX meta-rules-dat, mihomo's own
// default). Apps without the files download them from here before the profile starts.
var geoxURL = map[string]string{
	"geoip":   "https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/geoip-lite.dat",
	"geosite": "https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/geosite.dat",
	"mmdb":    "https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/geoip.metadb",
	"asn":     "https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/GeoLite2-ASN.mmdb",
}

// Render is a complete client profile.
func Render(p Profile, g model.GroupNames, m routing.Mode) ([]byte, error) {
	cfg, err := Config(p, g, m)
	if err != nil {
		return nil, err
	}
	return marshalYAML(cfg)
}

// Config is the profile Render writes.
func Config(p Profile, g model.GroupNames, m routing.Mode) (map[string]any, error) {
	if len(p.Proxies) == 0 {
		// Groups with no proxies in them are a profile mihomo may refuse whole.
		return nil, model.ErrNoProxies
	}
	return configOf(p, g, m), nil
}

// defaultNameservers are the DNS-over-HTTPS servers a profile resolves with, and the ones that
// resolve the proxy servers' own names.
var defaultNameservers = []string{"https://1.1.1.1/dns-query", "https://dns.google/dns-query"}

// configOf is the profile of p.Proxies, whatever they are.
func configOf(p Profile, g model.GroupNames, m routing.Mode) map[string]any {
	g = g.WithDefaults("")
	ps := p.Proxies
	proxies := make([]map[string]any, len(ps))
	names := make([]string, len(ps))
	for i, x := range ps {
		proxies[i], names[i] = p.proxy(x), x.Name
	}
	var countries []map[string]any
	if !p.Routes.Servers.NoCountries {
		countries = countryGroups(p.Nodes, ps, g, names)
	}
	selector := []string{g.Auto}
	for _, c := range countries {
		selector = append(selector, c["name"].(string))
	}
	auto := urlTest(g.Auto, names)
	if p.Routes.Servers.Auto == "fallback" {
		auto["type"] = "fallback" // the first that works, in the order of the nodes
		delete(auto, "tolerance")
	}
	groups := []map[string]any{
		{"name": g.Main, "type": "select", "proxies": append(selector, names...)},
		auto,
	}
	groups = append(groups, countries...)
	if n := p.Routes.Servers.Interval; n > 0 {
		for _, gr := range groups[1:] {
			gr["interval"] = n
		}
	}
	taken := map[string]bool{}
	for _, n := range append(append([]string{}, names...), g.Main, g.Auto, model.AliasGroup) {
		taken[strings.ToLower(n)] = true
	}
	for _, c := range countries {
		taken[strings.ToLower(c["name"].(string))] = true
	}
	if !p.Routable() && m == routing.Blocked {
		m = routing.RUDirect
	}
	rt := p.route(g, taken, m)
	groups = append(groups, rt.groups...)
	if g.Main != model.AliasGroup {
		groups = append(groups, map[string]any{"name": model.AliasGroup, "type": "select", "proxies": []string{g.Main}, "hidden": true})
	}
	dns := map[string]any{
		"enable": true, "ipv6": false, "enhanced-mode": "fake-ip", "fake-ip-range": "198.18.0.1/16",
		"default-nameserver": []string{"1.1.1.1", "8.8.8.8"},
		"nameserver":         slices.Clone(defaultNameservers),
	}
	// The panel and the nodes stay out of the tunnel whatever the admin's rules say.
	rules := append(directRules(p.Direct), "GEOIP,LAN,DIRECT,no-resolve")
	tune := p.Routes.Tune
	if tune.BlockQUIC {
		rules = append(rules, "AND,((NETWORK,UDP),(DST-PORT,443)),REJECT")
	}
	rules = append(rules, p.Rules...)
	rules = append(rules, rt.rules...)
	cfg := map[string]any{
		"mixed-port": 7890, "allow-lan": false, "mode": "rule", "log-level": "warning",
		// The node has no IPv6 on most VPS: with it on, apps first try IPv6 through the
		// tunnel and wait for the node's "network unreachable" before falling back.
		"ipv6": false, "unified-delay": true, "tcp-concurrent": true,
		"dns":          dns,
		"proxies":      proxies,
		"proxy-groups": groups,
	}
	if len(rt.providers) > 0 {
		cfg["rule-providers"] = rt.providers
	}
	if rt.geodata || m != routing.All {
		cfg["geodata-mode"], cfg["geox-url"] = false, geoxURL
	}
	if m == routing.RUDirect {
		rules = append(rules, "GEOSITE,category-ru,DIRECT", "GEOIP,ru,DIRECT")
	}
	if m != routing.All {
		// GEOIP,ru makes the app resolve every domain itself. DoH straight from Russia
		// stalls under TSPU throttling, so it goes through the tunnel (the alias group has
		// a fixed name: "&" or "=" in a renamed group would break the "#group" suffix).
		// Russian domains resolve with Yandex DNS directly and keep working without the VPN.
		dns["nameserver"] = []string{"https://1.1.1.1/dns-query#" + model.AliasGroup, "https://8.8.8.8/dns-query#" + model.AliasGroup}
		dns["proxy-server-nameserver"] = slices.Clone(defaultNameservers)
		dns["nameserver-policy"] = map[string]any{"geosite:category-ru": []string{"77.88.8.8", "77.88.8.1"}}
	}
	applyDNS(p.Routes.DNS, dns)
	if tune.RealIP {
		dns["enhanced-mode"] = "redir-host"
		delete(dns, "fake-ip-range")
	}
	if tune.Sniffer {
		cfg["sniffer"] = map[string]any{"enable": true, "parse-pure-ip": true,
			"sniff": map[string]any{"HTTP": map[string]any{"ports": []any{80, "8080-8880"}}, "TLS": map[string]any{"ports": []any{443, 8443}},
				"QUIC": map[string]any{"ports": []any{443, 8443}}}}
	}
	// mihomo skips a rule whose group can't carry UDP (a proxy with udp off, picked by hand
	// or by url-test) and sends what falls through to DIRECT: past the tunnel, from the real
	// address. REJECT makes such apps fall back to TCP instead. In the "blocked" mode the
	// rest goes direct by design; its tunnel rules get their own REJECT twins (rejectTwins).
	last := []string{"MATCH," + g.Main, "MATCH,REJECT"}
	if m == routing.Blocked {
		last = []string{"MATCH,DIRECT"}
	}
	cfg["rules"] = append(rules, last...)
	return cfg
}

func urlTest(name string, proxies []string) map[string]any {
	return map[string]any{"name": name, "type": "url-test", "proxies": proxies, "url": "https://www.gstatic.com/generate_204", "interval": 300, "tolerance": 50}
}

// countryGroups picks the fastest proxy of each node when there are several nodes. A
// node whose name would clash with another group or a proxy gets no group: the profile
// must load whatever the admin typed.
func countryGroups(nodes []model.Node, ps []model.Proxy, g model.GroupNames, names []string) []map[string]any {
	if len(nodes) < 2 {
		return nil
	}
	taken := map[string]bool{strings.ToLower(g.Main): true, strings.ToLower(g.Auto): true, strings.ToLower(model.AliasGroup): true}
	for _, n := range names {
		taken[strings.ToLower(n)] = true
	}
	var out []map[string]any
	for _, n := range nodes {
		var own []string
		for _, x := range ps {
			if x.Node == n.ID {
				own = append(own, x.Name)
			}
		}
		key := strings.ToLower(n.Name)
		if len(own) == 0 || model.ValidName(n.Name) != nil || taken[key] {
			continue
		}
		taken[key] = true
		out = append(out, urlTest(n.Name, own))
	}
	return out
}

// directRules keep traffic to the server itself (panel, SSH, subscription updates) out
// of the tunnel: in TUN mode it would otherwise loop through the node and die with it.
func directRules(hosts []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, h := range hosts {
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		switch ip, err := netip.ParseAddr(h); {
		case err != nil:
			out = append(out, "DOMAIN,"+h+",DIRECT")
		case ip.Is4():
			out = append(out, "IP-CIDR,"+ip.String()+"/32,DIRECT,no-resolve")
		default:
			out = append(out, "IP-CIDR6,"+ip.String()+"/128,DIRECT,no-resolve")
		}
	}
	return out
}

// Stub is a profile of one placeholder server named after why there are no others: the
// app shows it where the servers would be. main is the main group's name.
func Stub(name, main string) []byte {
	body, _ := marshalYAML(map[string]any{
		"proxies":      []map[string]any{{"name": name, "type": "socks5", "server": "127.0.0.1", "port": 1}},
		"proxy-groups": []map[string]any{{"name": main, "type": "select", "proxies": []string{name}}},
		"rules":        []string{"MATCH," + main},
	})
	return body
}
