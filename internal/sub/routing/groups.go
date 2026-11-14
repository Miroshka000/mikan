package routing

import (
	"strings"

	"mikan/internal/sub/model"
	"mikan/internal/textutil"
)

// Groups are the groups of the built-in profile, and the group each route's target goes to.
type Groups struct {
	List       []model.Group
	Main, Auto string
	// node is the group of one node of each route BuildGroups was given; "": none, the
	// route goes to the main group.
	node []string
}

// BuildGroups are the groups of the built-in profile over ps: the main group, where the user
// picks the automatic choice, a country or a server; the automatic choice (b) of the servers
// not kept out of it (autoMembers); with several nodes a group per country, the fastest of
// its servers, unless s.NoCountries; a group per route sent to a node of ps (routes are those
// of the intent the app takes, in order); and PROXY, the main group by the name Clash apps
// assume for it. A group whose name another has already is left out, and a route of one node
// goes to the main group then: the profile must load whatever the admin typed.
func BuildGroups(ps []model.Proxy, nodes []model.Node, g model.GroupNames, s Servers, b Balance, routes []Route, lang string) Groups {
	names := make([]string, len(ps))
	for i, x := range ps {
		names[i] = x.Name
	}
	var countries []model.Group
	if !s.NoCountries {
		countries = countryGroups(nodes, ps, g, names, b)
	}
	selector := []string{g.Auto}
	for _, c := range countries {
		selector = append(selector, c.Name)
	}
	auto := model.Group{Name: g.Auto, Type: b.Type, Members: autoMembers(ps, nodes, names), URL: b.URL, Interval: b.Interval}
	if b.Type == model.URLTest {
		auto.Tolerance = b.Tolerance
	}
	gs := Groups{Main: g.Main, Auto: g.Auto, node: make([]string, len(routes)),
		List: append([]model.Group{{Name: g.Main, Type: model.Select, Members: append(selector, names...)}, auto}, countries...)}
	taken := map[string]bool{}
	for _, n := range append(append([]string{}, names...), g.Main, g.Auto, model.AliasGroup) {
		taken[strings.ToLower(n)] = true
	}
	for _, c := range countries {
		taken[strings.ToLower(c.Name)] = true
	}
	for i, r := range routes {
		if r.To.Kind != ToNode {
			continue
		}
		var own []string
		for _, x := range ps {
			if x.Node == r.To.Node {
				own = append(own, x.Name)
			}
		}
		name := r.Icon + " " + r.Name
		if textutil.Lang(lang).En() {
			name = r.Icon + " " + r.NameEN
		}
		// A rule is split on commas: "ChatGPT, Claude, Gemini" as a target breaks the profile.
		name = strings.NewReplacer(", ", " · ", ",", " ").Replace(name)
		// A node this app has nothing of, or a name already taken: the main group.
		if len(own) == 0 || taken[strings.ToLower(name)] {
			continue
		}
		taken[strings.ToLower(name)] = true
		gs.List = append(gs.List, model.Group{Name: name, Type: model.Select, Members: append(own, g.Main)})
		gs.node[i] = name
	}
	if g.Main != model.AliasGroup {
		gs.List = append(gs.List, model.Group{Name: model.AliasGroup, Type: model.Select, Members: []string{g.Main}, Hidden: true})
	}
	return gs
}

// Of is the group that t, the target of route i of BuildGroups' (-1 for a rule of no route),
// goes to; "" for a policy (direct, reject, drop), which is no group.
func (gs Groups) Of(t Target, route int) string {
	switch t.Kind {
	case ToMain:
		return gs.Main
	case ToAlias:
		return model.AliasGroup
	case ToAuto:
		return gs.Auto
	case ToNode:
		if route >= 0 && route < len(gs.node) && gs.node[route] != "" {
			return gs.node[route]
		}
		return gs.Main
	case ToGroup:
		return t.Group
	}
	return ""
}

// Resolve is t, the target of route i of BuildGroups' (-1 for a rule of no route), as the
// group it goes to (ToGroup); a policy (direct, reject, drop) stays as it is.
func (gs Groups) Resolve(t Target, route int) Target {
	if name := gs.Of(t, route); name != "" {
		return Target{Kind: ToGroup, Group: name}
	}
	return t
}

// autoMembers are the names of ps the automatic choice takes: those not kept out of it, by
// their inbound (model.Proxy.NoAuto) or their node (model.Node.NoAuto). An empty choice
// breaks the profile, so with every server kept out it takes all of them, as before.
func autoMembers(ps []model.Proxy, nodes []model.Node, names []string) []string {
	out := map[int64]bool{}
	for _, n := range nodes {
		if n.NoAuto {
			out[n.ID] = true
		}
	}
	var in []string
	for i, x := range ps {
		if !x.NoAuto && !out[x.Node] {
			in = append(in, names[i])
		}
	}
	if len(in) == 0 || len(in) == len(names) {
		return names
	}
	return in
}

// countryGroups pick the fastest proxy of each node when there are several nodes. A node
// whose name would clash with another group or a proxy gets no group.
func countryGroups(nodes []model.Node, ps []model.Proxy, g model.GroupNames, names []string, b Balance) []model.Group {
	if len(nodes) < 2 {
		return nil
	}
	taken := map[string]bool{strings.ToLower(g.Main): true, strings.ToLower(g.Auto): true, strings.ToLower(model.AliasGroup): true}
	for _, n := range names {
		taken[strings.ToLower(n)] = true
	}
	var out []model.Group
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
		out = append(out, model.Group{Name: n.Name, Type: model.URLTest, Members: own, URL: b.URL, Interval: b.Interval, Tolerance: b.Tolerance})
	}
	return out
}
