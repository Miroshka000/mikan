package singbox

import (
	"context"
	"slices"
	"strings"
	"testing"

	"mikan/internal/sub/model"
	"mikan/internal/sub/routing"
	"mikan/internal/sub/subtest"
)

// The preview of an own Clash profile is told what sing-box makes of its groups: a
// load-balance becomes an urltest, the others stay as they are.
func TestNotes(t *testing.T) {
	src := strings.Replace(subtest.OwnTemplate, "  - name: 🚫 Без VPN\n",
		"  - name: ⚖️ Баланс\n    type: load-balance\n    include-all-proxies: true\n    strategy: round-robin\n  - name: 🚫 Без VPN\n", 1)
	if src == subtest.OwnTemplate {
		t.Fatal("the load-balance group is not in the profile")
	}
	prof := profileOf(subtest.Multi())
	want := []model.Note{{Core: "singbox", Subject: "⚖️ Баланс", Code: "balance_fastest"}}
	if got := Notes(prof, model.GroupNames{}, routing.All, subtest.Clash(src)); !slices.Equal(got, want) {
		t.Fatalf("notes: %+v", got)
	}
	if got := Notes(prof, model.GroupNames{}, routing.All, subtest.Clash(subtest.OwnTemplate)); len(got) != 0 {
		t.Fatalf("notes of groups sing-box has: %+v", got)
	}
}

// The preview is told what sing-box leaves out, and no list is downloaded for it: the
// proxies of a pinned certificate and a rule to a group of only them, a rule by an AS
// number, a binary list, a DNS policy of a negated category. The list it can read is not
// told, nor the category: sing-box names any geosite.
func TestDropped(t *testing.T) {
	singboxList = func(context.Context, string) ([]string, error) {
		t.Error("the preview downloads a list")
		return nil, nil
	}
	t.Cleanup(func() { singboxList = cachedList })
	prof := profileOf(subtest.Multi())
	var proxies []string
	var rest []model.Note
	for _, n := range Dropped(prof, model.GroupNames{}, routing.All, subtest.Clash(subtest.DroppedTemplate)) {
		if n.Code == "proxy_unsupported" {
			proxies = append(proxies, n.Subject)
		} else {
			rest = append(rest, n)
		}
	}
	if !slices.Contains(proxies, "🇳🇱 Hysteria2") || !slices.Contains(proxies, "🇺🇸 TUIC") || slices.Contains(proxies, "🇳🇱 VLESS Vision") {
		t.Fatalf("proxies: %q", proxies)
	}
	want := []model.Note{
		{Core: "singbox", Subject: "IP-ASN,13335", Code: "rule_unsupported"},
		{Core: "singbox", Subject: "ads", Code: "list_unsupported"},
		{Core: "singbox", Subject: "DOMAIN-SUFFIX,pinned.example,Закреплённые", Code: "rule_unsupported"},
		{Core: "singbox", Subject: "geosite:!cn", Code: "dns_policy_unsupported"},
	}
	if !slices.Equal(rest, want) {
		t.Fatalf("notes: %+v", rest)
	}
	// The groups' notes are apart: these groups sing-box has.
	if n := Notes(prof, model.GroupNames{}, routing.All, subtest.Clash(subtest.DroppedTemplate)); len(n) != 0 {
		t.Fatalf("group notes: %+v", n)
	}
	if n := Dropped(prof, model.GroupNames{}, routing.All, model.Template{Core: model.CoreSingBox, Body: subtest.OwnSingBoxTemplate}); n != nil {
		t.Fatalf("an own sing-box template: %+v", n)
	}
}

// Of the panel's routing, the admin's own lists are not downloaded for the preview either,
// and the ones sing-box can read are not told; DOMAIN-WILDCARD, which it has none of, is.
func TestDroppedBuiltin(t *testing.T) {
	singboxList = func(context.Context, string) ([]string, error) {
		t.Error("the preview downloads a list")
		return nil, nil
	}
	t.Cleanup(func() { singboxList = cachedList })
	u := subtest.Multi()
	u.Routes, u.Rules = subtest.Routes(), subtest.Rules
	for _, m := range subtest.Modes {
		var rest []model.Note
		for _, n := range Dropped(profileOf(u), model.GroupNames{}, m, model.Template{}) {
			if n.Code != "proxy_unsupported" {
				rest = append(rest, n)
			}
		}
		want := []model.Note{{Core: "singbox", Subject: "DOMAIN-WILDCARD,*.tinkoff.ru", Code: "rule_unsupported"}}
		if !slices.Equal(rest, want) {
			t.Errorf("%v: %+v", m, rest)
		}
	}
}
