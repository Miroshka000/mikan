package mihomo

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/metacubex/mihomo/config"

	"mikan/internal/sub/model"
	"mikan/internal/sub/routing"
	"mikan/internal/sub/subtest"
)

type groupView struct {
	Name    string   `json:"name"`
	Proxies []string `json:"proxies"`
}

type profileView struct {
	Proxies []map[string]any `json:"proxies"`
	Groups  []groupView      `json:"proxy-groups"`
	Rules   []string         `json:"rules"`
}

func readProfile(t *testing.T, raw []byte) profileView {
	t.Helper()
	if _, err := config.UnmarshalRawConfig(raw); err != nil {
		t.Fatalf("mihomo refuses the profile: %v\n%s", err, raw)
	}
	var v profileView
	if err := unmarshalProfile(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// The starter of a profile, rendered for the same user, is that profile: the groups have
// the same proxies, the rules are the same.
func TestStarterRoundTrip(t *testing.T) {
	prof := profileOf(subtest.TwoNodes())
	prof.Direct = []string{"203.0.113.7", "vpn.example.com"}
	prof.Routes = routing.Routes{Services: map[string]string{"youtube": routing.TargetVPN, "ai": subtest.NodeTarget(2)}, Direct: []string{"ru"}}
	want, err := Render(prof, model.GroupNames{}, routing.RUDirect)
	if err != nil {
		t.Fatal(err)
	}
	starter, err := Starter(prof, model.GroupNames{}, routing.RUDirect)
	if err != nil {
		t.Fatal(err)
	}
	s := string(starter)
	if strings.Contains(s, "uuid") || !strings.Contains(s, "include-all-proxies: true") || !strings.Contains(s, "mikan:") || strings.Contains(s, "203.0.113.7/32") {
		t.Fatalf("starter:\n%s", s)
	}
	got, _, err := Template(prof, s)
	if err != nil {
		t.Fatal(err)
	}
	w, g := readProfile(t, want), readProfile(t, got)
	if len(w.Proxies) != len(g.Proxies) {
		t.Fatalf("proxies: %d, want %d", len(g.Proxies), len(w.Proxies))
	}
	norm := func(gs []groupView) map[string]string {
		out := map[string]string{}
		for _, x := range gs {
			p := slices.Clone(x.Proxies)
			slices.Sort(p)
			out[x.Name] = strings.Join(p, "|")
		}
		return out
	}
	if a, b := norm(w.Groups), norm(g.Groups); len(a) != len(b) {
		t.Fatalf("groups:\n%v\nwant\n%v", b, a)
	} else {
		for name, members := range a {
			if b[name] != members {
				t.Errorf("group %s: %s, want %s", name, b[name], members)
			}
		}
	}
	rules := strings.Join(slices.DeleteFunc(slices.Clone(g.Rules), func(r string) bool { return r == "MATCH,REJECT" }), "\n")
	if rules != strings.Join(slices.DeleteFunc(slices.Clone(w.Rules), func(r string) bool { return r == "MATCH,REJECT" }), "\n") {
		t.Fatalf("rules:\n%s\nwant\n%s", rules, strings.Join(w.Rules, "\n"))
	}
}

func TestTemplateGroups(t *testing.T) {
	prof := profileOf(subtest.TwoNodes())
	prof.Direct = []string{"203.0.113.7"}
	raw, _, err := Template(prof, subtest.OwnTemplate)
	if err != nil {
		t.Fatal(err)
	}
	v := readProfile(t, raw)
	byName := map[string][]string{}
	for _, g := range v.Groups {
		byName[g.Name] = g.Proxies
	}
	types := map[string]string{}
	for _, p := range v.Proxies {
		types[p["name"].(string)] = p["type"].(string)
	}
	for _, n := range byName["⚡ Авто"] {
		if types[n] == "hysteria2" {
			t.Errorf("exclude-type left %s in", n)
		}
	}
	if us := byName["🇺🇸 США"]; len(us) != 1 || !strings.HasPrefix(us[0], "🇺🇸") {
		t.Errorf("filter: %v", us)
	}
	if jp := byName["🇯🇵 Япония"]; len(jp) != 1 || jp[0] != "REJECT" {
		t.Errorf("a group with nothing for this app must be closed: %v", jp)
	}
	for _, n := range byName["🔒 VLESS"] {
		if !strings.HasPrefix(n, "🇳🇱") || types[n] != "vless" {
			t.Errorf("mikan nodes and types: %v", byName["🔒 VLESS"])
		}
	}
	if len(byName["🔒 VLESS"]) == 0 {
		t.Error("mikan picked nothing")
	}
	if v.Rules[0] != "IP-CIDR,203.0.113.7/32,DIRECT,no-resolve" || v.Rules[len(v.Rules)-1] != "MATCH,REJECT" {
		t.Fatalf("the panel's rules around the admin's:\n%s", strings.Join(v.Rules, "\n"))
	}
	if strings.Contains(string(raw), "include-all-proxies") || strings.Contains(string(raw), "mikan:") {
		t.Fatalf("the asks stay out of the profile:\n%s", raw)
	}
	if err := CheckTemplate(prof, subtest.OwnTemplate); err != nil {
		t.Fatal(err)
	}
}

func TestCheckTemplate(t *testing.T) {
	prof := profileOf(subtest.Defaults(""))
	for src, code := range map[string]string{
		"proxy-groups: [":                   "template_yaml",
		"":                                  "template_empty",
		"proxy-groups:\n  - type: select\n": "template_group_name",
		"proxy-groups:\n  - {name: A, type: select, proxies: [B]}\n":                                                      "template_group_member",
		"proxy-groups:\n  - {name: A, type: select, proxies: [DIRECT]}\n  - {name: A, type: select, proxies: [DIRECT]}\n": "template_group_dup",
		"rules:\n  - MATCH,Nowhere\n":                  "template_rule_target",
		"rules:\n  - RULE-SET,nope,DIRECT\n":           "template_rule_set",
		"rules:\n  - DOMAIN-SUFFIX\n":                  "template_rule",
		"rules:\n  - SUB-RULE,(NETWORK,UDP),missing\n": "template_rule_target",
	} {
		err := CheckTemplate(prof, src)
		var te *model.TemplateError
		if !errors.As(err, &te) || te.Code != code {
			t.Errorf("%q: %v, want %s", src, err, code)
		}
	}
	ok := "sub-rules:\n  udp: [MATCH,DIRECT]\nrules:\n  - SUB-RULE,(NETWORK,UDP),udp\n  - DOMAIN,x.com,REJECT-DROP\n  - MATCH,DIRECT\n"
	if err := CheckTemplate(prof, ok); err != nil {
		t.Fatal(err)
	}
}

// A template checked on saving can still miss a user: one without the inbound a group
// names gets the built-in profile (an error the handler falls back on), never a profile
// mihomo refuses.
func TestTemplateIsCheckedForEachUser(t *testing.T) {
	prof := profileOf(subtest.TwoNodes())
	full, err := Render(prof, model.GroupNames{}, routing.All)
	if err != nil {
		t.Fatal(err)
	}
	name := readProfile(t, full).Proxies[len(readProfile(t, full).Proxies)-1]["name"].(string)
	src := "proxy-groups:\n  - {name: G, type: select, proxies: [\"" + name + "\"]}\nrules:\n  - MATCH,G\n"
	if _, _, err := Template(prof, src); err != nil {
		t.Fatalf("the user who has it: %v", err)
	}
	prof.Proxies = prof.Proxies[:len(prof.Proxies)-1] // the US node's inbound is not theirs
	var te *model.TemplateError
	if _, _, err := Template(prof, src); !errors.As(err, &te) || te.Code != "template_group_member" {
		t.Fatalf("a user without it: %v", err)
	}
}

// A filter only mihomo reads cannot go with mikan's choice of nodes: the panel could not
// apply the choice and the group would take every server.
func TestTemplateFilterGoCannotRead(t *testing.T) {
	prof := profileOf(subtest.TwoNodes())
	with := "proxy-groups:\n  - {name: G, type: select, mikan: {nodes: [2]}, filter: \"^(?=.*US)\"}\nrules:\n  - MATCH,G\n"
	var te *model.TemplateError
	if err := CheckTemplate(prof, with); !errors.As(err, &te) || te.Code != "template_filter" {
		t.Fatalf("mikan with a lookahead filter: %v", err)
	}
	without := "proxy-groups:\n  - {name: G, type: select, include-all-proxies: true, filter: \"^(?=.*US)\"}\nrules:\n  - MATCH,G\n"
	if err := CheckTemplate(prof, without); err != nil {
		t.Fatalf("mihomo applies the filter itself: %v", err)
	}
}
