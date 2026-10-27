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

// TemplateRules are the rules of the own Clash profile src as the settings make it, the same
// for every user, read apart from the core (routing.ReadRule): a placeholder stands for the
// user's proxies, which the rules do not depend on, and the panel's hosts go first as in
// every profile. A rule ReadRule cannot read (OR, NOT, SUB-RULE) is left out; false when
// src does not render.
func TemplateRules(p Profile, src string) ([]routing.Rule, bool) {
	p.Proxies = []model.Proxy{{Name: "mikan", Type: "direct"}}
	t, err := parseProfileTemplate(src)
	if err == nil {
		_, err = fillTemplate(t, p)
	}
	if err != nil {
		return nil, false
	}
	raw, _ := t["rules"].([]any)
	return readRules(raw), true
}

// readRules are the rules of a filled template that routing.ReadRule reads, in order.
func readRules(raw []any) []routing.Rule {
	var out []routing.Rule
	for _, x := range raw {
		if r, ok := routing.ReadRule(str(x)); ok {
			out = append(out, r)
		}
	}
	return out
}
