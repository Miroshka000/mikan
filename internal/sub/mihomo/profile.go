package mihomo

import (
	"mikan/internal/sub/apps"
	"mikan/internal/sub/model"
	"mikan/internal/sub/routing"
)

// Profile is what a mihomo profile is made of.
type Profile struct {
	Proxies []model.Proxy // the user's (build.Proxies)
	Nodes   []model.Node  // enabled nodes in display order
	Direct  []string      // hosts that bypass the tunnel: the panel and every node
	// Rules are the admin's own rules (routing.ServedRules) that the app's core parses
	// (RulesFor), before the built-in routing.
	Rules []routing.Rule
	// Routes go after the admin's rules, for the apps that take rule lists (App); Lang
	// names the groups they add.
	Routes routing.Routes
	App    apps.App
	Lang   string
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

// configOf is the profile of p.Proxies, whatever they are: the intent of the settings as
// the app takes it (routing.Intent), written the Clash way.
func configOf(p Profile, g model.GroupNames, m routing.Mode) map[string]any {
	g = g.WithDefaults("")
	in := routing.NewIntent(m, p.Rules, p.Routes)
	if !p.Routable() {
		in = in.Plain()
	}
	routes := p.taken(in.Routes)
	gs := routing.BuildGroups(p.Proxies, p.Nodes, g, in.Servers, in.Servers.MihomoBalance(), routes, p.Lang)
	proxies := make([]map[string]any, len(p.Proxies))
	for i, x := range p.Proxies {
		proxies[i] = p.proxy(x)
	}
	groups := make([]map[string]any, len(gs.List))
	for i, gr := range gs.List {
		groups[i] = groupOf(gr)
	}
	rt := route(routes, gs, in.Mode == routing.Blocked)
	// The panel and the nodes stay out of the tunnel whatever the admin's rules say.
	var rules []string
	for _, part := range [][]routing.Rule{routing.HostRules(p.Direct), in.Head(), in.Own} {
		for _, r := range part {
			rules = append(rules, r.Clash(targetName(gs, r.To, -1)))
		}
	}
	rules = append(rules, rt.rules...)
	for _, r := range in.Tail() {
		rules = append(rules, r.Clash(targetName(gs, r.To, -1)))
	}
	cfg := map[string]any{
		"mixed-port": 7890, "allow-lan": false, "mode": "rule", "log-level": "warning",
		// The node has no IPv6 on most VPS: with it on, apps first try IPv6 through the
		// tunnel and wait for the node's "network unreachable" before falling back.
		"ipv6": false, "unified-delay": true, "tcp-concurrent": true,
		"dns":          dnsOf(in.Resolvers()),
		"proxies":      proxies,
		"proxy-groups": groups,
	}
	if len(rt.providers) > 0 {
		cfg["rule-providers"] = rt.providers
	}
	if rt.geodata || in.Mode != routing.All {
		cfg["geodata-mode"], cfg["geox-url"] = false, routing.MetaCubeXGeoX
	}
	if in.Tune.Sniffer {
		cfg["sniffer"] = map[string]any{"enable": true, "parse-pure-ip": true,
			"sniff": map[string]any{"HTTP": map[string]any{"ports": []any{80, "8080-8880"}}, "TLS": map[string]any{"ports": []any{443, 8443}},
				"QUIC": map[string]any{"ports": []any{443, 8443}}}}
	}
	// mihomo skips a rule whose group can't carry UDP (a proxy with udp off, picked by hand
	// or by url-test) and sends what falls through to DIRECT: past the tunnel, from the real
	// address. REJECT makes such apps fall back to TCP instead. In the "blocked" mode the
	// rest goes direct by design; its tunnel rules get their own REJECT twins (rejectTwins).
	last := []string{"MATCH,DIRECT"}
	if final := in.Final(); final.Kind != routing.ToDirect {
		last = []string{"MATCH," + targetName(gs, final, -1), "MATCH,REJECT"}
	}
	cfg["rules"] = append(rules, last...)
	return cfg
}

// groupOf is gr as a Clash proxy group.
func groupOf(gr model.Group) map[string]any {
	m := map[string]any{"name": gr.Name, "type": string(gr.Type), "proxies": gr.Members}
	if gr.Type.Tests() {
		m["url"], m["interval"] = gr.URL, gr.Interval
		if gr.Tolerance > 0 && gr.Type == model.URLTest {
			m["tolerance"] = gr.Tolerance
		}
		if gr.Strategy != "" && gr.Type == model.LoadBalance {
			m["strategy"] = gr.Strategy
		}
	}
	if gr.Hidden {
		m["hidden"] = true
	}
	return m
}

// dnsOf is the DNS of a profile that resolves with r.
func dnsOf(r routing.Resolvers) map[string]any {
	dns := map[string]any{"enable": true, "ipv6": r.IPv6, "enhanced-mode": "fake-ip", "fake-ip-range": "198.18.0.1/16",
		"default-nameserver": r.Default, "nameserver": r.Nameserver}
	if len(r.ProxyServer) > 0 {
		dns["proxy-server-nameserver"] = r.ProxyServer
	}
	if len(r.Policy) > 0 {
		policy := map[string]any{}
		for _, x := range r.Policy {
			policy[x.Match] = x.Servers
		}
		dns["nameserver-policy"] = policy
	}
	if !r.FakeIP {
		dns["enhanced-mode"] = "redir-host"
		delete(dns, "fake-ip-range")
	}
	return dns
}

// directRules are routing.HostRules as Clash rules.
func directRules(hosts []string) []string {
	var out []string
	for _, r := range routing.HostRules(hosts) {
		out = append(out, r.Clash("DIRECT"))
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
