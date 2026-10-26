package xray

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"

	"mikan/internal/sub/apps"
	"mikan/internal/sub/mihomo"
	"mikan/internal/sub/model"
	"mikan/internal/sub/routing"
)

// The xray JSON profile is for the apps that run whole xray-core configs (apps.XrayJSON):
// a list of configs in place of share links, so that the panel's routing, DNS and
// automatic choice reach them. The list is the user's choice of a server: first a config
// per automatic group (the automatic choice, the countries, the ones of an own Clash
// profile), each a balancer xray tests the servers with, then a config per server. A
// config holds what it needs of the others: the servers its rules send a service to, and
// the balancers they name. An own xray template makes its list its own way (Template).

// The local proxies of a config, where the apps send what they tunnel.
const (
	socksPort = 10808
	httpPort  = 10809
)

// Render is the xray JSON profile of p made of the template t: an own xray one as the admin
// wrote it (Template), an own Clash one read as xray can, the panel's routing for none.
// model.ErrNoProxies when xray carries none of p's proxies; a template that does not render
// for p is a *model.TemplateError, and the caller decides what goes out in its place.
func Render(p mihomo.Profile, g model.GroupNames, m routing.Mode, t model.Template) ([]byte, error) {
	if t.Core == model.CoreXray {
		return Template(p, t.Body)
	}
	pl, err := planOf(p, g, m, t)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(pl.configs(), "", "  ")
}

// Notes are what xray makes of the groups of p's profile it cannot carry as they are
// (Balance), for the admin panel's preview: of the own Clash profile t, or of the built-in
// groups. The main group is no select in xray: its choices are the app's list of configs.
// An own xray template has its balancers as the admin wrote them: no notes.
func Notes(p mihomo.Profile, g model.GroupNames, m routing.Mode, t model.Template) []model.Note {
	if t.Core == model.CoreXray {
		return nil
	}
	pl, err := planOf(p, g, m, t)
	if err != nil {
		return nil
	}
	var out []model.Note
	for _, n := range Balance(pl.groups, pl.names(), pl.servers).Notes {
		if !pl.choice[n.Group] {
			out = append(out, n)
		}
	}
	return out
}

// plan is what the configs are made of, whether of the panel's routing or of an own profile.
type plan struct {
	outbounds []tagged
	groups    []model.Group // of the proxies' names
	rules     []rule
	final     routing.Target // where what no rule takes goes
	resolve   bool           // a rule matches an IP the app resolves a name for
	dns       routing.Resolvers
	servers   routing.Servers
	// choice are the names of the main group, which stands for the user's choice: in each
	// config it goes to the config's own server or balancer.
	choice map[string]bool
	// picks are the main group's choices (its members, or itself when it is no select), the
	// automatic ones of which get a config.
	picks []string
}

// tagged is a proxy with its outbound. Balancers select outbounds by the prefix of their
// tags, so the tags are numbered to one width: no tag begins with another.
type tagged struct {
	name, tag string
	out       map[string]any
}

// rule is a rule as xray carries it: one xray rule or two of the fields, to the target.
type rule struct {
	fields []map[string]any
	to     routing.Target
}

// planOf is the plan of the own Clash profile t for p, or of the panel's routing when t is
// none (or of another core's).
func planOf(p mihomo.Profile, g model.GroupNames, m routing.Mode, t model.Template) (plan, error) {
	g = g.WithDefaults(p.Lang)
	outs := outboundsOf(p.Proxies)
	if len(outs) == 0 {
		return plan{}, model.ErrNoProxies
	}
	// The groups are made of what xray carries: a group of nothing else is none.
	var carried []model.Proxy
	for _, x := range p.Proxies {
		if slices.ContainsFunc(outs, func(t tagged) bool { return t.name == x.Name }) {
			carried = append(carried, x)
		}
	}
	p.Proxies = carried
	if t.Core != model.CoreMihomo {
		return builtin(p, g, m, outs), nil
	}
	// The profile as a current mihomo would get it, as for sing-box: the rules of every list,
	// the ones xray has a form of are kept.
	p.App = apps.App{}
	parts, err := mihomo.TemplateParts(p, t.Body)
	if err != nil {
		return plan{}, err
	}
	return template(parts, g, outs, p.Routes.Servers), nil
}

// outboundsOf are the outbounds of the proxies xray carries, tagged.
func outboundsOf(ps []model.Proxy) []tagged {
	width := len(strconv.Itoa(len(ps)))
	var out []tagged
	for _, x := range ps {
		tag := fmt.Sprintf("proxy-%0*d", width, len(out)+1)
		if o, ok := outbound(x, tag); ok {
			out = append(out, tagged{name: x.Name, tag: tag, out: o})
		}
	}
	return out
}

// builtin is the plan of the panel's routing: the intent with what xray can match of it,
// and the built-in groups over it.
func builtin(p mihomo.Profile, g model.GroupNames, m routing.Mode, outs []tagged) plan {
	in := routing.NewIntent(m, p.Rules, p.Routes)
	// A route keeps the conditions xray matches; one of none makes no rule and no group.
	var routes []routing.Route
	for _, r := range in.Routes {
		var keep []routing.Match
		for _, x := range r.Rules {
			if fields([]routing.Match{x}) != nil {
				keep = append(keep, x)
			}
		}
		if len(keep) > 0 {
			r.Rules, r.Lists = keep, nil
			routes = append(routes, r)
		}
	}
	gs := routing.BuildGroups(p.Proxies, p.Nodes, g, in.Servers, in.Servers.XrayBalance(), routes, p.Lang)
	pl := plan{outbounds: outs, groups: gs.List, dns: in.Resolvers(), servers: in.Servers,
		choice: map[string]bool{g.Main: true, model.AliasGroup: true}}
	to := func(t routing.Target, route int) routing.Target {
		if name := gs.Of(t, route); name != "" {
			return routing.Target{Kind: routing.ToGroup, Group: name}
		}
		return t
	}
	for _, part := range [][]routing.Rule{routing.HostRules(p.Direct), in.Head(), in.Own} {
		for _, r := range part {
			pl.add(r.When, to(r.To, -1))
		}
	}
	for i, r := range routes {
		for _, x := range r.Rules {
			pl.add([]routing.Match{x}, to(r.To, i))
		}
	}
	for _, r := range in.Tail() {
		pl.add(r.When, to(r.To, -1))
	}
	pl.final = to(in.Final(), -1)
	pl.picks = gs.List[0].Members
	return pl
}

// template is the plan of an own Clash profile: its rules up to the first MATCH, its groups
// and DNS. Its main group is the one MATCH goes to, else the panel's main group by name,
// else its first.
func template(parts mihomo.Parts, g model.GroupNames, outs []tagged, s routing.Servers) plan {
	// What no rule takes goes direct in mihomo.
	pl := plan{outbounds: outs, groups: parts.Groups, dns: parts.DNS, servers: s, final: routing.Target{Kind: routing.ToDirect}}
	for _, r := range parts.Rules {
		if len(r.When) == 0 {
			pl.final = r.To
			break
		}
		pl.add(r.When, r.To)
	}
	byName := func(name string) (model.Group, bool) {
		i := slices.IndexFunc(parts.Groups, func(x model.Group) bool { return x.Name == name })
		if i < 0 {
			return model.Group{}, false
		}
		return parts.Groups[i], true
	}
	main, ok := byName(pl.final.Group)
	if pl.final.Kind != routing.ToGroup || !ok {
		if main, ok = byName(g.Main); !ok && len(parts.Groups) > 0 {
			main, ok = parts.Groups[0], true
		}
	}
	if !ok {
		return pl
	}
	pl.choice = map[string]bool{main.Name: true}
	// A select group goes to its first member, so one that starts with the main group (the
	// PROXY alias, a service sent to [PROXY, DIRECT]) follows the config's choice as well,
	// not the main group's first server.
	for grown := true; grown; {
		grown = false
		for _, gr := range parts.Groups {
			if gr.Type == model.Select && len(gr.Members) > 0 && pl.choice[gr.Members[0]] && !pl.choice[gr.Name] {
				pl.choice[gr.Name], grown = true, true
			}
		}
	}
	pl.picks = []string{main.Name}
	if main.Type == model.Select {
		pl.picks = main.Members
	}
	return pl
}

// add is a rule of the conditions to t, when xray matches them.
func (pl *plan) add(when []routing.Match, t routing.Target) {
	f := fields(when)
	if f == nil {
		return
	}
	pl.rules = append(pl.rules, rule{fields: f, to: t})
	pl.resolve = pl.resolve || resolves(when)
}

// names are the proxies xray carries, by the names the groups hold.
func (pl plan) names() map[string]bool {
	out := make(map[string]bool, len(pl.outbounds))
	for _, o := range pl.outbounds {
		out[o.name] = true
	}
	return out
}

// configs are the configs of the list: the automatic groups among the main group's choices
// (a group that is a balancer of its own, not a select of one), then the servers.
func (pl plan) configs() []map[string]any {
	b := pl.balancing()
	var out []map[string]any
	for _, name := range pl.picks {
		if r, ok := b.Refs[name]; ok && r.Balancer && r.Tag == name {
			out = append(out, pl.config(name, r, b))
		}
	}
	for _, o := range pl.outbounds {
		out = append(out, pl.config(o.name, Ref{Tag: o.tag}, b))
	}
	return out
}

// balancing is the groups as xray carries them, by the outbounds' tags.
func (pl plan) balancing() Balancing {
	b := Balance(pl.groups, pl.names(), pl.servers)
	tag := map[string]string{}
	for _, o := range pl.outbounds {
		tag[o.name] = o.tag
	}
	retag := func(s string) string {
		if t, ok := tag[s]; ok {
			return t
		}
		return s
	}
	tags := func(l []string) []string {
		out := make([]string, len(l))
		for i, s := range l {
			out[i] = retag(s)
		}
		return out
	}
	for i, x := range b.Balancers {
		x.Selector, x.FallbackTag = tags(x.Selector), retag(x.FallbackTag)
		b.Balancers[i] = x
	}
	for name, r := range b.Refs {
		if !r.Balancer {
			b.Refs[name] = Ref{Tag: retag(r.Tag)}
		}
	}
	dialers := make(map[string]string, len(b.Dialers))
	for o, d := range b.Dialers {
		dialers[retag(o)] = retag(d)
	}
	b.Dialers = dialers
	return b
}

// config is the whole config of the choice: the main group goes to it, named name.
func (pl plan) config(name string, choice Ref, b Balancing) map[string]any {
	via := func(group string) (Ref, bool) {
		if pl.choice[group] {
			return choice, true
		}
		r, ok := b.Refs[group]
		return r, ok
	}
	to := func(t routing.Target) (Ref, bool) {
		switch t.Kind {
		case routing.ToDirect:
			return Ref{Tag: directTag}, true
		case routing.ToReject, routing.ToDrop:
			return Ref{Tag: blockTag}, true
		case routing.ToGroup:
			return via(t.Group)
		}
		return choice, true
	}
	dns, rules := dnsOf(pl.dns, via, choice)
	// The apps' own queries go to xray's DNS, after its own queries went out above.
	rules = append(rules, map[string]any{"port": "53", "outboundTag": dnsOutTag})
	for _, r := range pl.rules {
		ref, ok := to(r.to)
		if !ok {
			continue // a group xray has none of
		}
		for _, f := range r.fields {
			rules = append(rules, withRef(f, ref))
		}
	}
	final, ok := to(pl.final)
	if !ok {
		final = choice
	}
	// xray sends what no rule takes to the first outbound: the last rule takes it all.
	rules = once(append(rules, withRef(map[string]any{"network": "tcp,udp"}, final)))

	used, balancers := uses(rules, b)
	outbounds := []map[string]any{}
	if !choice.Balancer {
		for _, o := range pl.outbounds {
			if o.tag == choice.Tag {
				outbounds = append(outbounds, dialed(o, b))
			}
		}
	}
	for _, o := range pl.outbounds {
		if used[o.tag] && o.tag != choice.Tag {
			outbounds = append(outbounds, dialed(o, b))
		}
	}
	outbounds = append(outbounds,
		map[string]any{"tag": directTag, "protocol": "freedom"},
		map[string]any{"tag": blockTag, "protocol": "blackhole"},
		map[string]any{"tag": dnsOutTag, "protocol": "dns"})
	strategy := "AsIs"
	if pl.resolve {
		strategy = "IPIfNonMatch"
	}
	rt := map[string]any{"domainStrategy": strategy, "rules": rules}
	if len(balancers) > 0 {
		rt["balancers"] = balancers
	}
	sniff := map[string]any{"enabled": true, "destOverride": []string{"http", "tls", "quic"}}
	out := map[string]any{
		"remarks": name,
		"log":     map[string]any{"loglevel": "warning"},
		"dns":     dns,
		"inbounds": []map[string]any{
			{"tag": "socks", "protocol": "socks", "listen": "127.0.0.1", "port": socksPort, "settings": map[string]any{"auth": "noauth", "udp": true}, "sniffing": sniff},
			{"tag": "http", "protocol": "http", "listen": "127.0.0.1", "port": httpPort, "sniffing": sniff},
		},
		"outbounds": outbounds,
		"routing":   rt,
	}
	observe(out, balancers, b)
	return out
}

// dnsOutTag is the outbound that answers the apps' queries with xray's DNS.
const dnsOutTag = "dns-out"

// once leaves out the rules of the conditions of a rule before them: xray takes the first
// rule that matches, so they would never be reached (a service and a list of one category).
func once(rules []map[string]any) []map[string]any {
	seen := map[string]bool{}
	out := rules[:0]
	for _, r := range rules {
		when := make(map[string]any, len(r))
		for k, v := range r {
			if k != "outboundTag" && k != "balancerTag" {
				when[k] = v
			}
		}
		key, _ := json.Marshal(when)
		if !seen[string(key)] {
			seen[string(key)] = true
			out = append(out, r)
		}
	}
	return out
}

// uses are the outbounds the rules send to, the ones of the balancers they name and the
// ones those dial through, and the balancers in the order Balance made them.
func uses(rules []map[string]any, b Balancing) (map[string]bool, []Balancer) {
	used, named := map[string]bool{}, map[string]bool{}
	for _, r := range rules {
		if t, ok := r["outboundTag"].(string); ok {
			used[t] = true
		}
		if t, ok := r["balancerTag"].(string); ok {
			named[t] = true
		}
	}
	var balancers []Balancer
	for _, x := range b.Balancers {
		if named[x.Tag] {
			balancers = append(balancers, x)
			for _, t := range x.Selector {
				used[t] = true
			}
		}
	}
	for t := range used {
		for d := b.Dialers[t]; d != "" && !used[d]; d = b.Dialers[d] {
			used[d] = true
		}
	}
	return used, balancers
}

// dialed is o's outbound, dialing through the hop before it in a chain.
func dialed(o tagged, b Balancing) map[string]any {
	d, ok := b.Dialers[o.tag]
	if !ok {
		return o.out
	}
	out := make(map[string]any, len(o.out))
	for k, v := range o.out {
		out[k] = v
	}
	ss := map[string]any{}
	if old, ok := o.out["streamSettings"].(map[string]any); ok {
		for k, v := range old {
			ss[k] = v
		}
	}
	ss["sockopt"] = map[string]any{"dialerProxy": d}
	out["streamSettings"] = ss
	return out
}

// observe tests the outbounds of the balancers a config has: the observatory for leastPing,
// the burst one for leastLoad, with Balance's address and interval.
func observe(cfg map[string]any, balancers []Balancer, b Balancing) {
	var ping, load []string
	for _, x := range balancers {
		switch x.Strategy.Type {
		case routing.XrayLeastPing:
			ping = append(ping, x.Selector...)
		case routing.XrayLeastLoad:
			load = append(load, x.Selector...)
		}
	}
	subjects := func(l []string) []string {
		slices.Sort(l)
		return slices.Compact(l)
	}
	if o := b.Observatory; o != nil && len(ping) > 0 {
		x := *o
		x.SubjectSelector = subjects(ping)
		cfg["observatory"] = x
	}
	if o := b.BurstObservatory; o != nil && len(load) > 0 {
		x := *o
		x.SubjectSelector = subjects(load)
		cfg["burstObservatory"] = x
	}
}
