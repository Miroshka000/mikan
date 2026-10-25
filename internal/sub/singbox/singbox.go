// Package singbox renders subscriptions for the sing-box apps that run a whole config
// (the official SFA, SFI, SFM and SFT). Their links-only subscription was no profile for
// them.
//
// The config is of sing-box 1.12 and later: type-based DNS servers and rule actions, the
// forms that version brought in and 1.14 requires. Its rules, rule sets and DNS are the
// panel's routing (routing.Intent) as sing-box writes it, so Settings → Routing reaches
// it; an own Clash profile is read the same way (mihomo.TemplateParts). The proxies are
// mihomo's, whose fields sing-box reads. What sing-box has no counterpart for is left out:
// XHTTP and the mihomo-only protocols (build.ForApp leaves them out already), a self-signed
// certificate pinned by its hash (sing-box pins a public key, from 1.13 on), .mrs lists,
// rule types of mihomo's own, logic rules but AND.
package singbox

import (
	"context"
	"encoding/json"
	"errors"

	"mikan/internal/sub/apps"
	"mikan/internal/sub/mihomo"
	"mikan/internal/sub/model"
	"mikan/internal/sub/routing"
)

// Since is the first sing-box with the config's shape.
var Since = apps.Version{1, 12, 0}

// The keepalive of a gRPC transport: an idle connection is pinged after grpcIdleTimeout and
// given up when no answer comes within grpcPingTimeout, so that the app does not go on
// sending into one a middlebox cut while it was silent.
const (
	grpcIdleTimeout = "15s"
	grpcPingTimeout = "10s"
)

// Render is the sing-box config of p: of the own Clash profile src when there is one (and
// it renders for p), of the panel's routing otherwise.
func Render(ctx context.Context, p mihomo.Profile, g model.GroupNames, m routing.Mode, src string) ([]byte, error) {
	pl, err := planOf(p, g, m, src)
	if err != nil {
		return nil, err
	}
	out := config(ctx, pl)
	if out == nil {
		return nil, model.ErrNoProxies // nothing of the profile sing-box can carry
	}
	return json.MarshalIndent(out, "", "  ")
}

// Stub is a config of one placeholder outbound named after why there are no others: the
// app shows it where the servers would be. main is the main group's name.
func Stub(name, main string) []byte {
	body, _ := json.MarshalIndent(map[string]any{
		"outbounds": []map[string]any{{"type": "selector", "tag": main, "outbounds": []string{name}},
			{"type": "socks", "tag": name, "server": "127.0.0.1", "server_port": 1}},
		"route": map[string]any{"final": main},
	}, "", "  ")
	return body
}

// plan is what a config is made of, whether of the panel's routing or of an own profile.
type plan struct {
	proxies []map[string]any // as mihomo writes them
	groups  []model.Group
	// rules are in order, each to a policy or a group (routing.ToGroup); a rule of no
	// conditions (MATCH) ends them.
	rules []routing.Rule
	lists map[string]routing.Provider
	dns   routing.Resolvers
	main  string
}

// planOf is the plan of the own profile src for p, or of the panel's routing when there is
// none or it does not render for p (Clash apps get the built-in profile then too).
func planOf(p mihomo.Profile, g model.GroupNames, m routing.Mode, src string) (plan, error) {
	g = g.WithDefaults(p.Lang)
	if len(p.Proxies) == 0 {
		return plan{}, model.ErrNoProxies
	}
	if src != "" {
		// The profile as a current mihomo would get it: the rules of every list, for the
		// lists to be converted (a list of a younger core has no sing-box counterpart anyway).
		p.App = apps.App{}
		parts, err := mihomo.TemplateParts(p, src)
		if err == nil {
			return plan{proxies: parts.Proxies, groups: parts.Groups, rules: parts.Rules, lists: parts.Lists, dns: parts.DNS, main: g.Main}, nil
		}
		if errors.Is(err, model.ErrNoProxies) {
			return plan{}, err
		}
	}
	return builtin(p, g, m), nil
}

// builtin is the plan of the panel's routing: the intent with the lists sing-box can read,
// and the built-in groups over it.
func builtin(p mihomo.Profile, g model.GroupNames, m routing.Mode) plan {
	in := routing.NewIntent(m, p.Rules, p.Routes)
	var routes []routing.Route
	for _, r := range in.Routes {
		// A list of a younger mihomo type or format (routing.Provider.Since) has no sing-box
		// counterpart, and neither have the conditions that read it.
		have := map[string]bool{}
		var lists []routing.Provider
		for _, pr := range r.Lists {
			if !pr.Since.Known() {
				have[pr.Name] = true
				lists = append(lists, pr)
			}
		}
		var rules []routing.Match
		for _, x := range r.Rules {
			if x.Type != "RULE-SET" || have[x.Value] {
				rules = append(rules, x)
			}
		}
		if len(rules) > 0 {
			r.Rules, r.Lists = rules, lists
			routes = append(routes, r)
		}
	}
	gs := routing.BuildGroups(p.Proxies, p.Nodes, g, in.Servers, in.Servers.SingBoxBalance(), routes, p.Lang)
	pl := plan{groups: gs.List, lists: map[string]routing.Provider{}, dns: in.Resolvers(), main: g.Main}
	for _, x := range p.Proxies {
		pl.proxies = append(pl.proxies, mihomo.Proxy(x))
	}
	to := func(t routing.Target, route int) routing.Target {
		if name := gs.Of(t, route); name != "" {
			return routing.Target{Kind: routing.ToGroup, Group: name}
		}
		return t
	}
	for _, part := range [][]routing.Rule{routing.HostRules(p.Direct), in.Head(), in.Own} {
		for _, r := range part {
			pl.rules = append(pl.rules, routing.Rule{When: r.When, To: to(r.To, -1)})
		}
	}
	for i, r := range routes {
		for _, pr := range r.Lists {
			pl.lists[pr.Name] = pr
		}
		for _, x := range r.Rules {
			pl.rules = append(pl.rules, routing.Rule{When: []routing.Match{x}, To: to(r.To, i)})
		}
	}
	for _, r := range in.Tail() {
		pl.rules = append(pl.rules, routing.Rule{When: r.When, To: to(r.To, -1)})
	}
	pl.rules = append(pl.rules, routing.Rule{To: to(in.Final(), -1)})
	return pl
}

func config(ctx context.Context, pl plan) map[string]any {
	var outbounds []map[string]any
	tags := map[string]bool{"direct": true}
	for _, x := range pl.proxies {
		if o, ok := singboxOutbound(x); ok {
			outbounds = append(outbounds, o)
			tags[str(o["tag"])] = true
		}
	}
	if len(outbounds) == 0 {
		return nil
	}
	gs := convertGroups(pl.groups, tags)
	for _, gr := range gs.outbounds {
		tags[str(gr["tag"])] = true
	}
	for _, o := range outbounds {
		if d, ok := gs.detours[str(o["tag"])]; ok && tags[d] {
			o["detour"] = d
		}
	}
	main := pl.main
	if !tags[main] {
		main = ""
		if len(gs.outbounds) > 0 {
			main = str(gs.outbounds[0]["tag"])
		}
	}
	// The lists download through the tunnel: GitHub is slow or cut off where it is needed.
	detour := main
	if detour == "" {
		detour = str(outbounds[0]["tag"])
	}
	sets := ruleSets{ctx: ctx, detour: detour, lists: pl.lists, used: map[string]map[string]any{}}
	var rules []map[string]any
	final := ""
	for _, r := range pl.rules {
		to, ok := action(r.To, tags)
		if len(r.When) == 0 {
			// The first MATCH ends the rules, as in mihomo; one that rejects leaves the rest
			// to the main group.
			if ok && to["outbound"] != nil {
				final = str(to["outbound"])
			}
			break
		}
		if !ok {
			continue // a group sing-box has none of
		}
		match, ok := sets.rule(r)
		if !ok {
			continue
		}
		for k, v := range to {
			match[k] = v
		}
		rules = append(rules, match)
	}
	if final == "" {
		final = main
	}
	dns, resolver := dnsOf(pl.dns, tags, &sets)
	all := append(append([]map[string]any{}, gs.outbounds...), outbounds...)
	all = append(all, map[string]any{"type": "direct", "tag": "direct"})
	route := map[string]any{
		"rules":                 append([]map[string]any{{"action": "sniff"}, {"protocol": "dns", "action": "hijack-dns"}}, rules...),
		"auto_detect_interface": true, "default_domain_resolver": resolver,
	}
	if final != "" {
		route["final"] = final
	}
	if len(sets.used) > 0 {
		route["rule_set"] = sets.sets()
	}
	return map[string]any{
		"log": map[string]any{"level": "warn"},
		"dns": dns,
		"inbounds": []map[string]any{{"type": "tun", "tag": "tun-in", "address": []string{"172.19.0.1/30"},
			"auto_route": true, "strict_route": true}},
		"outbounds":    all,
		"route":        route,
		"experimental": map[string]any{"cache_file": map[string]any{"enabled": true}},
	}
}
