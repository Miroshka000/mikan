package mihomo

import (
	"strings"

	"mikan/internal/sub/profile"
	"mikan/internal/sub/routing"
)

// takes says whether the app's core reads a list.
func takes(p profile.Profile, pr routing.Provider) bool {
	return !pr.Since.Known() || p.App.Core.Known() && p.App.Core.AtLeast(pr.Since)
}

// taken are the routes with what the app's core takes of them: a list it cannot read goes,
// and so do the conditions that read it; a route left with none goes too.
func taken(p profile.Profile, routes []routing.Route) []routing.Route {
	var out []routing.Route
	for _, r := range routes {
		var lists []routing.Provider
		have := map[string]bool{}
		for _, pr := range r.Lists {
			if takes(p, pr) {
				lists = append(lists, pr)
				have[pr.Name] = true
			}
		}
		var rules []routing.Match
		for _, m := range r.Rules {
			if m.Type != "RULE-SET" || have[m.Value] {
				rules = append(rules, m)
			}
		}
		if len(rules) > 0 {
			r.Rules, r.Lists = rules, lists
			out = append(out, r)
		}
	}
	return out
}

// routed is what the routes add to a profile.
type routed struct {
	providers map[string]any
	rules     []string
	geodata   bool // a GEOSITE or GEOIP rule: the app needs mihomo's geodata
}

// route writes the routes (taken) with the groups they go to; blocked is the mode of the
// blocked lists.
func route(routes []routing.Route, gs routing.Groups, blocked bool) routed {
	out := routed{providers: map[string]any{}}
	for i, r := range routes {
		for _, pr := range r.Lists {
			ext := pr.Format
			if ext == "" {
				ext = "yaml"
			}
			// Through the tunnel: GitHub is slow or cut off where the VPN is needed.
			out.providers[pr.Name] = map[string]any{"type": "http", "behavior": pr.Behavior, "format": pr.Format, "url": pr.URL,
				"path": "./ruleset/" + pr.Name + "." + ext, "interval": 86400, "proxy": gs.Main}
		}
		target := targetName(gs, r.To, i)
		for _, m := range r.Rules {
			out.geodata = out.geodata || m.Type == "GEOSITE" || m.Type == "GEOIP"
			out.rules = append(out.rules, m.Rule(target))
		}
	}
	if blocked {
		// The lists name sites by GEOSITE too.
		out.geodata = true
		out.rules = rejectTwins(out.rules)
	}
	return out
}

// targetName is where t, of route i (-1: of no route), goes in a Clash profile: a policy or
// a group.
func targetName(gs routing.Groups, t routing.Target, i int) string {
	switch t.Kind {
	case routing.ToDirect:
		return "DIRECT"
	case routing.ToReject:
		return "REJECT"
	case routing.ToDrop:
		return "REJECT-DROP"
	}
	return gs.Of(t, i)
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
