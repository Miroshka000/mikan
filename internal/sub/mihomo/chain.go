package mihomo

import (
	"strings"

	"mikan/internal/sub/model"
)

// checkChains refuses, on saving, the chains Citrus refuses on loading a profile (its
// config/chain.go), in the profile as the panel fills it. Citrus wires a chain by setting
// the dialer-proxy of what each hop past the first is made of, through the hop before it:
// the proxy itself, every member of a group (and the proxies of its providers), the first
// hop of a chain inside it. So a proxy dials through one hop wherever it is used, and
// Citrus refuses:
//
//   - a chain of fewer than two hops, a hop twice, the chain as its own hop
//     (template_chain_hops);
//   - a chain that takes hops other than from proxies: use, include-all, filter and the
//     like (template_chain_keys; checkChainKeys refuses them, and the panel's mikan ask, in
//     the template as written, since the panel fills or drops some of them);
//   - DIRECT, REJECT and the other built-ins past the first hop, or in a group past the
//     first hop (template_chain_builtin);
//   - a group with include-all past the first hop (template_chain_group), and one that
//     uses a provider there is none of (template_group_member);
//   - a proxy or a provider that two chains need through different hops, or that has a
//     dialer-proxy of its own (template_chain_dialer);
//   - a loop through a chain, as written or made by the wiring (template_chain_loop).
//
// The profile itself is left as it is: Citrus wires it on loading. A template saved before
// renders as it did (CheckTemplate is not run with each profile); sing-box and xray keep
// the first hop a proxy is wired through there (model.Wire, chain_dialer_taken).
func checkChains(cfg map[string]any) error {
	cg := chainGraph{proxies: map[string]map[string]any{}, groups: map[string]map[string]any{}, dialer: map[string]string{}}
	cg.providers, _ = cfg["proxy-providers"].(map[string]any)
	var chains []map[string]any
	groups, _ := cfg["proxy-groups"].([]any)
	for _, x := range groups {
		if m, ok := x.(map[string]any); ok {
			cg.groups[str(m["name"])] = m
			if str(m["type"]) == string(model.Chain) {
				chains = append(chains, m)
			}
		}
	}
	if len(chains) == 0 {
		return nil
	}
	proxies, _ := cfg["proxies"].([]any)
	for _, x := range proxies {
		if m, ok := x.(map[string]any); ok {
			cg.proxies[str(m["name"])] = m
			if d := str(m["dialer-proxy"]); d != "" {
				cg.dialer["proxy\x00"+str(m["name"])] = d
			}
		}
	}
	for name, x := range cg.providers {
		if m, ok := x.(map[string]any); ok {
			if d := str(m["dialer-proxy"]); d != "" {
				cg.dialer["provider\x00"+name] = d
			}
		}
	}

	for _, c := range chains {
		name, hops := str(c["name"]), list(c["proxies"])
		if len(hops) < 2 {
			return templateErr("template_chain_hops", name)
		}
		if err := askedHops(c); err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, hop := range hops {
			if hop == name || seen[hop] {
				return templateErr("template_chain_hops", name)
			}
			seen[hop] = true
		}
	}
	// Loops as written, then the ones the wiring makes (Citrus looks twice too).
	for _, c := range chains {
		if err := cg.loop(str(c["name"])); err != nil {
			return err
		}
	}
	for _, c := range chains {
		name, hops := str(c["name"]), list(c["proxies"])
		for i := 1; i < len(hops); i++ {
			if err := cg.wire(name, hops[i], hops[i-1], "", map[string]bool{}); err != nil {
				return err
			}
		}
	}
	for _, c := range chains {
		if err := cg.loop(str(c["name"])); err != nil {
			return err
		}
	}
	return nil
}

// checkChainKeys refuses a chain of the template as written that asks for its hops: Citrus
// takes them from proxies only, and the panel's fill (include-all-proxies, mikan, a filter
// it drops) would make a chain of whatever proxies a user has, of one hop for some.
func checkChainKeys(cfg map[string]any) error {
	groups, _ := cfg["proxy-groups"].([]any)
	for _, x := range groups {
		if m, ok := x.(map[string]any); ok && str(m["type"]) == string(model.Chain) {
			if err := askedHops(m); err != nil {
				return err
			}
		}
	}
	return nil
}

// askedHops refuses the chain c when it asks for hops other than by proxies.
func askedHops(c map[string]any) error {
	for _, k := range []string{"use", "include-all", "include-all-proxies", "include-all-providers", "filter", "exclude-filter", "exclude-type", "mikan"} {
		if _, ok := c[k]; ok {
			return templateErr("template_chain_keys", str(c["name"]))
		}
	}
	return nil
}

// chainGraph is the filled profile by names, and the dialer-proxy of each proxy and provider
// (by kind and name) as Citrus would wire it.
type chainGraph struct {
	proxies   map[string]map[string]any
	groups    map[string]map[string]any
	providers map[string]any
	dialer    map[string]string
}

// wire makes name, a hop of the chain or a member of the group parent of that hop, dial
// through via.
func (cg chainGraph) wire(chain, name, via, parent string, visiting map[string]bool) error {
	if visiting[name] {
		return nil
	}
	visiting[name] = true
	if _, ok := cg.proxies[name]; ok {
		return cg.set(chain, "proxy\x00"+name, name, via)
	}
	g, ok := cg.groups[name]
	if !ok {
		// a built-in: the template's members are checked already (checkFilled)
		return templateErr("template_chain_builtin", chain+" → "+name)
	}
	if str(g["type"]) == string(model.Chain) {
		hops := list(g["proxies"])
		if len(hops) == 0 {
			return templateErr("template_chain_hops", name)
		}
		return cg.wire(chain, hops[0], via, name, visiting)
	}
	for _, k := range []string{"include-all", "include-all-proxies", "include-all-providers"} {
		if g[k] == true {
			return templateErr("template_chain_group", chain+" → "+name)
		}
	}
	for _, m := range list(g["proxies"]) {
		if err := cg.wire(chain, m, via, name, visiting); err != nil {
			return err
		}
	}
	for _, pd := range list(g["use"]) {
		m, ok := cg.providers[pd].(map[string]any)
		if !ok {
			return templateErr("template_group_member", name+" → "+pd)
		}
		if ov, ok := m["override"].(map[string]any); ok {
			if d := str(ov["dialer-proxy"]); d != "" && d != via {
				return templateErr("template_chain_dialer", chain+" → "+pd)
			}
		}
		if err := cg.set(chain, "provider\x00"+pd, pd, via); err != nil {
			return err
		}
	}
	return nil
}

// set wires the proxy or provider key (its name name) through via: refused when it dials
// through another hop already.
func (cg chainGraph) set(chain, key, name, via string) error {
	if d, ok := cg.dialer[key]; ok && d != via {
		return templateErr("template_chain_dialer", chain+" → "+name)
	}
	cg.dialer[key] = via
	return nil
}

// edges are what name dials through: a proxy's dialer-proxy, a group's members and its
// providers' dialer-proxy (the override's first).
func (cg chainGraph) edges(name string) []string {
	if _, ok := cg.proxies[name]; ok {
		if d := cg.dialer["proxy\x00"+name]; d != "" {
			return []string{d}
		}
		return nil
	}
	g, ok := cg.groups[name]
	if !ok {
		return nil
	}
	out := list(g["proxies"])
	for _, pd := range list(g["use"]) {
		m, _ := cg.providers[pd].(map[string]any)
		d := cg.dialer["provider\x00"+pd]
		if ov, ok := m["override"].(map[string]any); ok && str(ov["dialer-proxy"]) != "" {
			d = str(ov["dialer-proxy"])
		}
		if d != "" {
			out = append(out, d)
		}
	}
	return out
}

// loop refuses a loop reachable from the chain start.
func (cg chainGraph) loop(start string) error {
	const (
		onPath = iota + 1
		done
	)
	state := map[string]int{}
	var path []string
	var visit func(name string) []string
	visit = func(name string) []string {
		switch state[name] {
		case onPath:
			for i, n := range path {
				if n == name {
					return append(append([]string{}, path[i:]...), name)
				}
			}
		case done:
			return nil
		}
		state[name] = onPath
		path = append(path, name)
		for _, next := range cg.edges(name) {
			if l := visit(next); l != nil {
				return l
			}
		}
		path = path[:len(path)-1]
		state[name] = done
		return nil
	}
	if l := visit(start); l != nil {
		return templateErr("template_chain_loop", strings.Join(l, " → "))
	}
	return nil
}
