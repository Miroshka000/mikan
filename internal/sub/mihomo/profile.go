package mihomo

import (
	_ "embed"

	"mikan/internal/sub/model"
	"mikan/internal/sub/profile"
	"mikan/internal/sub/routing"
)

// base is base.yaml, the start of every built-in profile, and baseKeys its keys in the
// order of the file; sniffer is sniffer.yaml.
var (
	//go:embed base.yaml
	baseYAML []byte
	//go:embed sniffer.yaml
	snifferYAML []byte

	base, baseKeys = embeddedYAML(baseYAML)
	sniffer, _     = embeddedYAML(snifferYAML)
)

// Render is a complete client profile.
func Render(p profile.Profile, g model.GroupNames, m routing.Mode) ([]byte, error) {
	cfg, err := renderConfig(p, g, m)
	if err != nil {
		return nil, err
	}
	return marshalYAML(cfg)
}

// renderConfig is the profile Render writes: the intent of the settings as the app takes
// it (routing.Intent), written the Clash way.
func renderConfig(p profile.Profile, g model.GroupNames, m routing.Mode) (map[string]any, error) {
	if len(p.Proxies) == 0 {
		// Groups with no proxies in them are a profile mihomo may refuse whole.
		return nil, model.ErrNoProxies
	}
	g = g.WithDefaults("")
	in := routing.NewIntent(m, p.Rules, p.Routes)
	if !p.App.TakesOwnProfile() {
		in = in.Plain()
	}
	routes := routing.Narrow(in.Routes, func(pr routing.Provider) bool { return takes(p, pr) })
	gs := routing.BuildGroups(p.Proxies, p.Nodes, g, in.Servers, in.Servers.MihomoBalance(), routes, p.Lang)
	proxies := make([]map[string]any, len(p.Proxies))
	for i, x := range p.Proxies {
		proxies[i] = proxy(p, x)
	}
	groups := make([]map[string]any, len(gs.List))
	for i, gr := range gs.List {
		groups[i] = groupOf(gr)
	}
	// The panel and the nodes stay out of the tunnel whatever the admin's rules say.
	pre, routedRules, tail, final := in.Sequence(p.Direct, routes, gs.Resolve)
	rt := route(routes, routedRules, gs, in.Mode == routing.Blocked)
	var rules []string
	for _, r := range pre {
		rules = append(rules, r.Clash(targetName(gs, r.To, -1)))
	}
	rules = append(rules, rt.rules...)
	for _, r := range tail {
		rules = append(rules, r.Clash(targetName(gs, r.To, -1)))
	}
	cfg := cloneYAML(base).(map[string]any)
	dnsOf(cfg["dns"].(map[string]any), in.Resolvers())
	cfg["proxies"], cfg["proxy-groups"] = proxies, groups
	if len(rt.providers) > 0 {
		cfg["rule-providers"] = rt.providers
	}
	if !rt.geodata && in.Mode == routing.All {
		// Rules that name no geodata do not make the app download it.
		delete(cfg, "geodata-mode")
		delete(cfg, "geox-url")
	}
	if in.Tune.Sniffer {
		cfg["sniffer"] = cloneYAML(sniffer)
	}
	// mihomo skips a rule whose group can't carry UDP (a proxy with udp off, picked by hand
	// or by url-test) and sends what falls through to DIRECT: past the tunnel, from the real
	// address. REJECT makes such apps fall back to TCP instead. In the "blocked" mode the
	// rest goes direct by design; its tunnel rules get their own REJECT twins (rejectTwins).
	last := []string{"MATCH,DIRECT"}
	if final.Kind != routing.ToDirect {
		last = []string{"MATCH," + targetName(gs, final, -1), "MATCH,REJECT"}
	}
	cfg["rules"] = append(rules, last...)
	return cfg, nil
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

// dnsOf fills dns, the DNS of base.yaml, for a profile that resolves with r.
func dnsOf(dns map[string]any, r routing.Resolvers) {
	dns["ipv6"], dns["default-nameserver"], dns["nameserver"] = r.IPv6, r.Default, r.Nameserver
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
