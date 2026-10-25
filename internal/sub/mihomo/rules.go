package mihomo

import (
	"mikan/internal/sub/apps"
	"mikan/internal/sub/model"
	"mikan/internal/sub/routing"
)

// ruleSince is the first mihomo with a rule type younger than the cores still around.
var ruleSince = map[string]apps.Version{"DOMAIN-REGEX": {1, 18, 2}, "IP-ASN": {1, 18, 2}, "SRC-GEOIP": {1, 18, 4}, "DOMAIN-WILDCARD": {1, 19, 12}}

// RulesFor are the rules an app's core parses. A rule it does not know fails the whole
// profile, so a younger type goes only to a mihomo app that names a core with it.
func RulesFor(rules []routing.Rule, app apps.App) []routing.Rule {
	ok := func(r routing.Rule) bool {
		for _, m := range r.When {
			if since, young := ruleSince[m.Type]; young && !(app.Family == apps.FamilyMihomo && app.Core.Known() && app.Core.AtLeast(since)) {
				return false
			}
		}
		return true
	}
	for i, r := range rules {
		if !ok(r) {
			out := append([]routing.Rule{}, rules[:i]...)
			for _, r := range rules[i+1:] {
				if ok(r) {
					out = append(out, r)
				}
			}
			return out
		}
	}
	return rules
}

// Rules are the rules of the Clash profile the settings make (an own one, src, when it
// renders), the same for every user: a placeholder stands for the user's proxies, which the
// rules do not depend on. p carries no proxies of its own.
func Rules(p Profile, g model.GroupNames, m routing.Mode, src string) []string {
	p.Proxies = []model.Proxy{{Name: "mikan", Type: "direct"}}
	var rules []any
	if src != "" {
		t, err := parseProfileTemplate(src)
		if err == nil {
			_, err = fillTemplate(t, p)
		}
		if err == nil {
			rules, _ = t["rules"].([]any)
		}
	}
	if rules == nil {
		for _, r := range configOf(p, g, m)["rules"].([]string) {
			rules = append(rules, r)
		}
	}
	out := make([]string, 0, len(rules))
	for _, r := range rules {
		out = append(out, str(r))
	}
	return out
}
