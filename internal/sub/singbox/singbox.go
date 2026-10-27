// Package singbox renders subscriptions for the sing-box apps that run a whole config
// (the official SFA, SFI, SFM and SFT). Their links-only subscription was no profile for
// them.
//
// The config is of sing-box 1.12 and later: type-based DNS servers and rule actions, the
// forms that version brought in and 1.14 requires. Its rules, rule sets and DNS are the
// panel's routing (routing.Intent) as sing-box writes it, so Settings → Routing reaches
// it; an own Clash profile is read the same way (mihomo.TemplateParts), and an own sing-box
// template is the config as the admin wrote it (Template). The proxies are mihomo's, whose
// fields sing-box reads. What sing-box has no counterpart for is left out:
// XHTTP and the mihomo-only protocols (build.ForApp leaves them out already), a self-signed
// certificate pinned by its hash (sing-box pins a public key, from 1.13 on), .mrs lists,
// rule types of mihomo's own, logic rules but AND.
package singbox

import (
	"context"
	"encoding/json"

	"mikan/internal/sub/apps"
	"mikan/internal/sub/mihomo"
	"mikan/internal/sub/model"
	"mikan/internal/sub/profile"
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

// Render is the sing-box config of p made of the template t: an own sing-box one as the
// admin wrote it, an own Clash one read as sing-box can, the panel's routing for none. A
// template that does not render for p is a *model.TemplateError: the caller decides what
// goes out in its place.
func Render(ctx context.Context, p profile.Profile, g model.GroupNames, m routing.Mode, t model.Template) ([]byte, error) {
	if t.Core == model.CoreSingBox {
		return Template(p, t.Body)
	}
	pl, err := planOf(p, g, m, t)
	if err != nil {
		return nil, err
	}
	out := config(pl, &ruleSets{ctx: ctx})
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
	// tunnelDNS sends the first nameserver through the main group: mihomo leaves the names
	// of a fake-ip profile to the tunnel, sing-box resolves them itself, and DoH straight
	// from Russia stalls under TSPU throttling.
	tunnelDNS bool
	main      string
}

// planOf is the plan of the own Clash profile t for p, or of the panel's routing when t is
// none (or of another core's).
func planOf(p profile.Profile, g model.GroupNames, m routing.Mode, t model.Template) (plan, error) {
	g = g.WithDefaults(p.Lang)
	if len(p.Proxies) == 0 {
		return plan{}, model.ErrNoProxies
	}
	if t.Core != model.CoreMihomo {
		return builtin(p, g, m), nil
	}
	// The profile as a current mihomo would get it: the rules of every list, for the lists to
	// be converted (a list of a younger core has no sing-box counterpart anyway).
	p.App = apps.App{}
	parts, err := mihomo.TemplateParts(p, t.Body)
	if err != nil {
		return plan{}, err
	}
	return plan{proxies: parts.Proxies, groups: parts.Groups, rules: parts.Rules, lists: parts.Lists, dns: parts.DNS, main: g.Main}, nil
}

// builtin is the plan of the panel's routing: the intent with the lists sing-box can read,
// and the built-in groups over it.
func builtin(p profile.Profile, g model.GroupNames, m routing.Mode) plan {
	in := routing.NewIntent(m, p.Rules, p.Routes)
	// A list of a younger mihomo type or format (routing.Provider.Since) has no sing-box
	// counterpart, and neither have the conditions that read it.
	routes := routing.Narrow(in.Routes, func(pr routing.Provider) bool { return !pr.Since.Known() })
	gs := routing.BuildGroups(p.Proxies, p.Nodes, g, in.Servers, in.Servers.SingBoxBalance(), routes, p.Lang)
	pl := plan{groups: gs.List, lists: map[string]routing.Provider{}, dns: in.Resolvers(), main: g.Main}
	// The panel's own nameservers only: one the admin gave without "#group" goes as given.
	pl.tunnelDNS = pl.dns.FakeIP && len(in.DNS.Nameserver) == 0
	for _, x := range p.Proxies {
		pl.proxies = append(pl.proxies, mihomo.Proxy(x))
	}
	for _, r := range routes {
		for _, pr := range r.Lists {
			pl.lists[pr.Name] = pr
		}
	}
	pre, routed, tail, final := in.Sequence(p.Direct, routes, gs.Resolve)
	pl.rules = append(append(append(pre, routed...), tail...), routing.Rule{To: final})
	return pl
}

// config is the config of pl, its lists read by sets (whose ctx they download with). Of the
// preview's sets (Dropped) it is walked for what it leaves out, which sets tell.
func config(pl plan, sets *ruleSets) map[string]any {
	var outbounds []map[string]any
	tags := map[string]bool{"direct": true}
	for _, x := range pl.proxies {
		if o, ok := singboxOutbound(x); ok {
			outbounds = append(outbounds, o)
			tags[str(o["tag"])] = true
		} else {
			sets.drop("proxy_unsupported", str(x["name"]))
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
	sets.detour, sets.lists, sets.used = detour, pl.lists, map[string]map[string]any{}
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
			sets.drop("rule_unsupported", r.Clash(r.To.Group)) // a group sing-box has none of
			continue
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
	via := ""
	if pl.tunnelDNS {
		via = model.AliasGroup // as the other modes' nameservers go
		if !tags[via] {
			via = main
		}
	}
	dns, resolver := dnsOf(pl.dns, tags, sets, via)
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
