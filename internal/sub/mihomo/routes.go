package mihomo

import (
	"strings"

	"mikan/internal/sub/apps"
	"mikan/internal/sub/model"
	"mikan/internal/sub/routing"
	"mikan/internal/textutil"
)

// Routable: the app takes rule lists and the rules of routing.Services. Stash and apps we
// do not know get the profile without them; a zero App (the admin panel's preview) is
// mihomo.
func (p Profile) Routable() bool {
	return p.App.Family == "" || p.App.Family == apps.FamilyMihomo
}

// takes says whether the app's core reads a list.
func (p Profile) takes(pr routing.Provider) bool {
	return !pr.Since.Known() || p.App.Core.Known() && p.App.Core.AtLeast(pr.Since)
}

// routed is what Routes adds to a profile.
type routed struct {
	groups    []map[string]any
	providers map[string]any
	rules     []string
	geodata   bool // a GEOSITE or GEOIP rule: the app needs mihomo's geodata
}

func (p Profile) route(g model.GroupNames, taken map[string]bool, m routing.Mode) routed {
	var out routed
	if !p.Routable() {
		return out
	}
	out.providers = map[string]any{}
	use := func(pr routing.Provider) bool {
		if !p.takes(pr) {
			return false
		}
		ext := pr.Format
		if ext == "" {
			ext = "yaml"
		}
		// Through the tunnel: GitHub is slow or cut off where the VPN is needed.
		out.providers[pr.Name] = map[string]any{"type": "http", "behavior": pr.Behavior, "format": pr.Format, "url": pr.URL,
			"path": "./ruleset/" + pr.Name + "." + ext, "interval": 86400, "proxy": g.Main}
		return true
	}
	for _, s := range routing.Services {
		t, ok := p.Routes.Services[s.ID]
		if !ok {
			continue
		}
		target := p.serviceTarget(s, t, g, taken, &out)
		have := map[string]bool{}
		for _, pr := range s.Providers {
			have[pr.Name] = use(pr)
		}
		for _, r := range s.Rules {
			if r.Type == "RULE-SET" && !have[r.Value] {
				continue
			}
			out.geodata = out.geodata || r.Type == "GEOSITE" || r.Type == "GEOIP"
			out.rules = append(out.rules, r.Rule(target))
		}
	}
	for _, l := range p.Routes.Lists {
		pr := routing.Provider{Name: routing.ListProvider(l.Name), Behavior: l.Behavior, Format: l.Format, URL: l.URL}
		if l.Format == "mrs" {
			pr.Since = routing.MRSSince
		}
		if !use(pr) {
			continue
		}
		target := p.serviceTarget(routing.Service{Icon: "📋", Name: l.Name, NameEN: l.Name}, l.Target, g, taken, &out)
		rule := "RULE-SET," + pr.Name + "," + target
		if l.Behavior == "ipcidr" {
			rule += ",no-resolve"
		}
		out.rules = append(out.rules, rule)
	}
	for _, id := range p.Routes.Direct {
		set, _ := routing.DirectSetByID(id)
		for _, pr := range set.Providers {
			if use(pr) {
				out.rules = append(out.rules, "RULE-SET,"+pr.Name+",DIRECT")
			}
		}
	}
	if m == routing.Blocked {
		for _, pr := range routing.BlockedLists {
			if use(pr) {
				out.rules = append(out.rules, "RULE-SET,"+pr.Name+","+g.Main)
			}
		}
		// The lists name sites by GEOSITE too.
		out.geodata = true
		out.rules = rejectTwins(out.rules)
	}
	return out
}

// rejectTwins follows every rule into the tunnel with the same match to REJECT. mihomo
// skips a rule whose group cannot carry UDP (a proxy with udp off) and here the profile
// ends with MATCH,DIRECT: QUIC to a blocked site would go out from the real address. The twin
// stops it there, and the app falls back to TCP through the tunnel.
func rejectTwins(rules []string) []string {
	out := make([]string, 0, 2*len(rules))
	for _, r := range rules {
		out = append(out, r)
		// TYPE,VALUE,TARGET[,no-resolve]: anything else (a comma in the value) stays as it is.
		parts := strings.Split(r, ",")
		if len(parts) < 3 || len(parts) > 4 || len(parts) == 4 && parts[3] != "no-resolve" {
			continue
		}
		if target := parts[2]; target == "DIRECT" || strings.HasPrefix(target, "REJECT") {
			continue
		}
		parts[2] = "REJECT"
		out = append(out, strings.Join(parts, ","))
	}
	return out
}

// serviceTarget is the policy or group a service's rules go to; a group of one node is
// added to out.
func (p Profile) serviceTarget(s routing.Service, t string, g model.GroupNames, taken map[string]bool, out *routed) string {
	switch t {
	case routing.TargetDirect:
		return "DIRECT"
	case routing.TargetBlock:
		return "REJECT"
	}
	id, ok := routing.NodeOf(t)
	if !ok {
		return g.Main
	}
	var own []string
	for _, x := range p.Proxies {
		if x.Node == id {
			own = append(own, x.Name)
		}
	}
	name := s.Icon + " " + s.Name
	if textutil.Lang(p.Lang).En() {
		name = s.Icon + " " + s.NameEN
	}
	// A rule is split on commas: "ChatGPT, Claude, Gemini" as a target breaks the profile.
	name = strings.NewReplacer(", ", " · ", ",", " ").Replace(name)
	// A node this app has nothing of, or a name already taken: the main group.
	if len(own) == 0 || taken[strings.ToLower(name)] {
		return g.Main
	}
	taken[strings.ToLower(name)] = true
	out.groups = append(out.groups, map[string]any{"name": name, "type": "select", "proxies": append(own, g.Main)})
	return name
}

// applyDNS puts the admin's resolvers over the panel's own.
func applyDNS(d routing.DNS, dns map[string]any) {
	if d.Empty() {
		return
	}
	for key, list := range map[string][]string{"nameserver": d.Nameserver, "default-nameserver": d.Default, "proxy-server-nameserver": d.ProxyServer} {
		if len(list) > 0 {
			dns[key] = list
		}
	}
	if len(d.Policy) > 0 {
		policy := map[string]any{}
		for _, x := range d.Policy {
			policy[x.Match] = x.Servers
		}
		dns["nameserver-policy"] = policy
	}
}
