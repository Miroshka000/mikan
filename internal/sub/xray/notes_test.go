package xray

import (
	"slices"
	"testing"

	"mikan/internal/sub/model"
	"mikan/internal/sub/routing"
	"mikan/internal/sub/subtest"
)

// The preview is told what xray leaves out: the proxies it cannot carry, a rule by an AS
// number, the lists it has no categories for, a category the usual geodata lacks and a DNS
// policy of a negated category. A pinned Hysteria2 it carries, and the rule to a group of
// it stays.
func TestDropped(t *testing.T) {
	prof := profileOf(subtest.Multi())
	proxies, rest := split(Dropped(prof, model.GroupNames{}, routing.All, subtest.Clash(subtest.DroppedTemplate)))
	if !slices.Contains(proxies, "🇺🇸 TUIC") || slices.Contains(proxies, "🇳🇱 Hysteria2") {
		t.Fatalf("proxies: %q", proxies)
	}
	want := []model.Note{
		{Core: "xray", Subject: "IP-ASN,13335", Code: "rule_unsupported"},
		{Core: "xray", Subject: "ads", Code: "list_unsupported"},
		{Core: "xray", Subject: "ru", Code: "list_unsupported"},
		{Core: "xray", Subject: "geosite:no-such-category", Code: "category_missing"},
		{Core: "xray", Subject: "geosite:!cn", Code: "dns_policy_unsupported"},
	}
	if !slices.Equal(rest, want) {
		t.Fatalf("notes: %+v", rest)
	}
	// The groups' notes are apart.
	groups := []model.Note{{Core: "xray", Subject: "Закреплённые", Code: "select_first"}}
	if n := Notes(prof, model.GroupNames{}, routing.All, subtest.Clash(subtest.DroppedTemplate)); !slices.Equal(n, groups) {
		t.Fatalf("group notes: %+v", n)
	}
	if n := Dropped(prof, model.GroupNames{}, routing.All, model.Template{Core: model.CoreXray, Body: subtest.OwnXrayTemplate}); n != nil {
		t.Fatalf("an own xray template: %+v", n)
	}
}

// Of the panel's routing, the conditions a route loses are told as well: its own lists and
// the lists of apps, which have no categories, once each however many routes have them.
func TestDroppedBuiltin(t *testing.T) {
	u := subtest.Multi()
	u.Routes, u.Rules = subtest.Routes(), subtest.Rules
	_, rest := split(Dropped(profileOf(u), model.GroupNames{}, routing.RUDirect, model.Template{}))
	for _, name := range []string{"own-wl", "own-ads", "mikan-ru-apps"} {
		if !slices.Contains(rest, model.Note{Core: "xray", Subject: name, Code: "list_unsupported"}) {
			t.Errorf("%s is not told: %+v", name, rest)
		}
	}
	seen := map[model.Note]bool{}
	for _, n := range rest {
		if seen[n] {
			t.Errorf("a note twice: %+v", n)
		}
		seen[n] = true
	}
	// DOMAIN-WILDCARD and the DNS policy of +.cn xray has a form of.
	for _, n := range rest {
		if n.Code != "list_unsupported" {
			t.Errorf("told: %+v", n)
		}
	}
}

// split are the notes of proxies, by name, and the rest.
func split(notes []model.Note) (proxies []string, rest []model.Note) {
	for _, n := range notes {
		if n.Code == "proxy_unsupported" {
			proxies = append(proxies, n.Subject)
		} else {
			rest = append(rest, n)
		}
	}
	return proxies, rest
}
