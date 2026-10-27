package mihomo

import (
	"slices"
	"strconv"

	"mikan/internal/sub/model"
	"mikan/internal/sub/profile"
	"mikan/internal/sub/routing"
)

// Parts are the admin's own Clash profile filled for a user, read apart from the core: for
// a core that writes them its own way (sing-box), as it writes the panel's routing.
type Parts struct {
	// Proxies are the profile's in its order: the template's own the model holds
	// (ReadProxy), then the panel's that went in.
	Proxies []model.Proxy
	Groups  []model.Group
	// Rules are in order; MATCH reads as a rule of no conditions. A rule ReadRule cannot read
	// (OR, NOT) is left out.
	Rules []routing.Rule
	// Lists are the rule-providers the apps download, by name.
	Lists map[string]routing.Provider
	DNS   routing.Resolvers
}

// TemplateParts are the parts of the profile Template makes of src for p.
func TemplateParts(p profile.Profile, src string) (Parts, error) {
	cfg, in, err := templateConfig(p, src)
	if err != nil {
		return Parts{}, err
	}
	var out Parts
	all := anyList(cfg["proxies"])
	for _, x := range all[:len(all)-len(in)] { // the template's own come first
		if m, ok := x.(map[string]any); ok {
			if y, ok := ReadProxy(m); ok {
				out.Proxies = append(out.Proxies, y)
			}
		}
	}
	out.Proxies = append(out.Proxies, in...)
	for _, x := range anyList(cfg["proxy-groups"]) {
		if g, ok := groupOfMap(x); ok {
			out.Groups = append(out.Groups, g)
		}
	}
	out.Rules = readRules(anyList(cfg["rules"]))
	out.Lists = map[string]routing.Provider{}
	providers, _ := cfg["rule-providers"].(map[string]any)
	for name, x := range providers {
		m, _ := x.(map[string]any)
		if u := str(m["url"]); u != "" {
			out.Lists[name] = routing.Provider{Name: name, Behavior: str(m["behavior"]), Format: str(m["format"]), URL: u}
		}
	}
	out.DNS = resolversOf(cfg["dns"])
	return out, nil
}

// groupOfMap reads a group of a filled template; false for one without a name.
func groupOfMap(x any) (model.Group, bool) {
	m, _ := x.(map[string]any)
	g := model.Group{Name: str(m["name"]), Type: model.GroupType(str(m["type"])), Members: list(m["proxies"]),
		URL: str(m["url"]), Strategy: str(m["strategy"]), Hidden: m["hidden"] == true}
	if g.Name == "" {
		return g, false
	}
	g.Interval, g.Tolerance = number(m["interval"]), number(m["tolerance"])
	return g, true
}

// number is a number the YAML holds as a number or a string; 0 for none.
func number(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case string:
		n, _ := strconv.Atoi(x)
		return n
	}
	return 0
}

// resolversOf reads the DNS of a template: the policy's servers in the order of its names,
// as the profile writes them.
func resolversOf(v any) routing.Resolvers {
	d, _ := v.(map[string]any)
	r := routing.Resolvers{Nameserver: list(d["nameserver"]), Default: list(d["default-nameserver"]), ProxyServer: list(d["proxy-server-nameserver"]),
		FakeIP: str(d["enhanced-mode"]) != "redir-host", IPv6: d["ipv6"] != false}
	policy, _ := d["nameserver-policy"].(map[string]any)
	keys := make([]string, 0, len(policy))
	for k := range policy {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if servers := list(policy[k]); len(servers) > 0 {
			r.Policy = append(r.Policy, routing.DNSPolicy{Match: k, Servers: servers})
		}
	}
	return r
}

func anyList(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case []map[string]any:
		out := make([]any, len(x))
		for i, m := range x {
			out[i] = m
		}
		return out
	case []string:
		out := make([]any, len(x))
		for i, s := range x {
			out[i] = s
		}
		return out
	}
	return nil
}
