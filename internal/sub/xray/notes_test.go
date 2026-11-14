package xray

import (
	"fmt"
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

// A Clash profile's rule-provider named after a panel's list is matched by the geodata's
// categories of it: one the usual geodata has none of (ru-blocked) is told, one it has
// (category-ru) is not, and its rule stays.
func TestDroppedListWithoutGeodata(t *testing.T) {
	if s, ips := routing.Common.List("mikan-blocked"); len(s)+len(ips) != 0 {
		t.Fatalf("the usual geodata has mikan-blocked: %q %q", s, ips)
	}
	if s, _ := routing.Common.List("mikan-ru-core"); len(s) == 0 {
		t.Fatal("the usual geodata has no mikan-ru-core")
	}
	const src = `mode: rule
proxy-groups:
  - name: Сеть
    type: select
    include-all-proxies: true
rule-providers:
  mikan-blocked:
    type: http
    behavior: classical
    url: https://example.com/mikan-blocked.yaml
    path: ./mikan-blocked.yaml
  mikan-ru-core:
    type: http
    behavior: domain
    url: https://example.com/mikan-ru-core.yaml
    path: ./mikan-ru-core.yaml
rules:
  - RULE-SET,mikan-blocked,Сеть
  - RULE-SET,mikan-ru-core,DIRECT
  - MATCH,Сеть
`
	prof := profileOf(subtest.Multi())
	_, rest := split(Dropped(prof, model.GroupNames{}, routing.All, subtest.Clash(src)))
	if want := []model.Note{{Core: "xray", Subject: "mikan-blocked", Code: "list_unsupported"}}; !slices.Equal(rest, want) {
		t.Fatalf("notes: %+v", rest)
	}
	pl, err := planOf(prof, model.GroupNames{}, routing.All, subtest.Clash(src))
	if err != nil || len(pl.rules) == 0 || fmt.Sprint(pl.rules[len(pl.rules)-1].fields) != "[map[domain:[geosite:category-ru]]]" {
		t.Fatalf("rules: %+v %v", pl.rules, err)
	}
}

// A profile of no proxy xray carries renders to nothing (model.ErrNoProxies): the preview is
// still told of each of them.
func TestDroppedNoProxies(t *testing.T) {
	prof := profileOf(subtest.Multi())
	unsupported, _ := split(Dropped(prof, model.GroupNames{}, routing.All, model.Template{}))
	prof.Proxies = slices.DeleteFunc(prof.Proxies, func(x model.Proxy) bool { return !slices.Contains(unsupported, x.Name) })
	if len(prof.Proxies) == 0 {
		t.Fatal("no proxy xray cannot carry")
	}
	for _, tpl := range []model.Template{{}, subtest.Clash(subtest.DroppedTemplate)} {
		if proxies, rest := split(Dropped(prof, model.GroupNames{}, routing.All, tpl)); !slices.Equal(proxies, unsupported) || len(rest) != 0 {
			t.Errorf("%q: proxies %q, rest %+v", tpl.Core, proxies, rest)
		}
	}
}
