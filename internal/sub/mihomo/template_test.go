package mihomo

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"mikan/internal/sub/model"
	"mikan/internal/sub/profile"
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
	coreAccepts(t, raw)
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
		"rules:\n  - MATCH,Nowhere\n":                                       "template_rule_target",
		"rules:\n  - RULE-SET,nope,DIRECT\n":                                "template_rule_set",
		"rules:\n  - DOMAIN-SUFFIX\n":                                       "template_rule",
		"rules:\n  - SUB-RULE,(NETWORK,UDP),missing\n":                      "template_rule_target",
		"proxy-groups:\n  - {name: A, type: relay, proxies: [DIRECT]}\n":    "template_group_type",
		"proxy-groups:\n  - {name: A, type: url_test, proxies: [DIRECT]}\n": "template_group_type",
		"proxy-groups:\n  - {name: A, proxies: [DIRECT]}\n":                 "template_group_type",
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
	// A panel without inbounds has nothing to check the groups and rules with, as with
	// xray: the template is read, and saved without the rest of the check.
	if err := CheckTemplate(profile.Profile{}, "proxy-groups:\n  - {name: G, type: select, include-all-proxies: true}\n"); !errors.Is(err, model.ErrNoProxies) {
		t.Fatalf("no proxies: %v, want ErrNoProxies", err)
	}
	var te *model.TemplateError
	if err := CheckTemplate(profile.Profile{}, "proxy-groups: ["); !errors.As(err, &te) || te.Code != "template_yaml" {
		t.Fatalf("no proxies, broken YAML: %v, want template_yaml", err)
	}
	if err := CheckTemplate(profile.Profile{}, "proxy-groups:\n  - {name: R, type: relay, proxies: [DIRECT]}\n"); !errors.As(err, &te) || te.Code != "template_group_type" || te.Detail != "R" {
		t.Fatalf("no proxies, a relay group: %v, want template_group_type of R", err)
	}
	chain := "proxy-groups:\n  - {name: C, type: chain, proxies: [DIRECT], include-all-proxies: true}\n  - {name: S, type: select, proxies: [C]}\nrules:\n  - MATCH,S\n"
	if err := CheckTemplate(prof, chain); err != nil {
		t.Fatalf("a chain: %v", err)
	}
}

// A type of group the core does not know is refused only on saving: a template saved
// before keeps rendering, as it is.
func TestTemplateUnknownGroupTypeStillRenders(t *testing.T) {
	prof := profileOf(subtest.Defaults(""))
	src := "proxy-groups:\n  - {name: R, type: relay, include-all-proxies: true}\nrules:\n  - MATCH,R\n"
	raw, _, err := Template(prof, src)
	if err != nil {
		t.Fatalf("a saved template with a relay group: %v", err)
	}
	if !strings.Contains(string(raw), "type: relay") {
		t.Fatalf("the group's type is the admin's:\n%s", raw)
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

// Next to proxy-providers, include-all would also take every proxy of the user, past the
// nodes "mikan" picked: the group keeps only the providers, and its filters stay for them.
func TestTemplateIncludeAllNextToProviders(t *testing.T) {
	prof := profileOf(subtest.TwoNodes())
	const providers = "proxy-providers:\n  p1: {type: http, url: \"http://127.0.0.1:1/p1.yaml\", path: ./providers/p1.yaml, interval: 3600}\n"
	for _, c := range []struct {
		name, group string
		drop        func(model.Proxy) bool // the NL proxies the group leaves out
		want        string                 // the group's keys besides name, type and proxies
	}{
		{"include-all", providers + "{name: G, type: select, mikan: {nodes: [1]}, include-all: true}",
			nil, "map[include-all-providers:true]"},
		{"include-all with filters", providers + "{name: G, type: select, mikan: {nodes: [1]}, include-all: true, filter: \"^🇳🇱\", exclude-type: hysteria2}",
			func(x model.Proxy) bool { return typeOf(x) == "hysteria2" }, "map[exclude-type:hysteria2 filter:^🇳🇱 include-all-providers:true]"},
		{"use with filters", providers + "{name: G, type: select, mikan: {nodes: [1]}, use: [p1], filter: \"^🇳🇱\", exclude-filter: TUIC}",
			func(x model.Proxy) bool { return strings.Contains(x.Name, "TUIC") }, "map[exclude-filter:TUIC filter:^🇳🇱 use:[p1]]"},
		{"include-all-providers with a filter", providers + "{name: G, type: select, mikan: {nodes: [1]}, include-all-providers: true, filter: \"^🇳🇱\"}",
			nil, "map[filter:^🇳🇱 include-all-providers:true]"},
		{"include-all-proxies and use with a filter", providers + "{name: G, type: select, include-all-proxies: true, use: [p1], filter: \"^🇳🇱\"}",
			nil, "map[filter:^🇳🇱 use:[p1]]"},
		// Nothing of the user's, but the providers': no REJECT for select to start on.
		{"nothing picked", providers + "{name: G, type: select, mikan: {nodes: [1]}, include-all: true, filter: \"^none\"}",
			func(model.Proxy) bool { return true }, "map[filter:^none include-all-providers:true]"},
		{"no providers", "{name: G, type: select, mikan: {nodes: [1]}, include-all: true, filter: \"^🇳🇱\", exclude-type: hysteria2}",
			func(x model.Proxy) bool { return typeOf(x) == "hysteria2" }, "map[]"},
		// mihomo has no providers to give such a group.
		{"empty proxy-providers, nothing picked", "proxy-providers: {}\n{name: G, type: select, mikan: {nodes: [1]}, include-all: true, filter: \"^none\"}",
			func(model.Proxy) bool { return true }, "map[]"},
		{"no providers, nothing picked", "{name: G, type: select, mikan: {nodes: [1]}, include-all: true, filter: \"^none\"}",
			func(model.Proxy) bool { return true }, "map[]"},
	} {
		t.Run(c.name, func(t *testing.T) {
			src := strings.Replace(c.group, "{name: G", "proxy-groups:\n  - {name: G", 1) + "\nrules:\n  - MATCH,G\n"
			raw, _, err := Template(prof, src)
			if err != nil {
				t.Fatal(err)
			}
			coreAccepts(t, raw)
			var v struct {
				Groups []map[string]any `json:"proxy-groups"`
			}
			if err := unmarshalProfile(raw, &v); err != nil {
				t.Fatal(err)
			}
			g := v.Groups[0]
			var want []any
			for _, x := range prof.Proxies {
				if x.Node == 1 && (c.drop == nil || !c.drop(x)) {
					want = append(want, x.Name)
				}
			}
			if len(want) == 0 && !strings.HasPrefix(c.group, providers) {
				want = []any{"REJECT"} // closed, not open
			}
			if got, _ := g["proxies"].([]any); !slices.Equal(got, want) {
				t.Errorf("proxies: %v, want %v", got, want)
			}
			delete(g, "name")
			delete(g, "type")
			delete(g, "proxies")
			if got := fmt.Sprint(g); got != c.want {
				t.Errorf("group: %s, want %s\n%s", got, c.want, raw)
			}
		})
	}
}

// The parts of a template keep each rule-provider's interval, written as a number or a
// string; a provider of none has zero.
func TestTemplatePartsListInterval(t *testing.T) {
	src := strings.Replace(subtest.OwnTemplate, "    path: ./ru.yaml\n", `    path: ./ru.yaml
    interval: 3600
  ip:
    type: http
    behavior: ipcidr
    url: https://example.com/ip.yaml
    interval: "7200"
  apps:
    type: http
    behavior: classical
    url: https://example.com/apps.yaml
`, 1)
	if src == subtest.OwnTemplate {
		t.Fatal("the lists are not in the template")
	}
	parts, err := TemplateParts(profileOf(subtest.TwoNodes()), src)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]int{"ru": 3600, "ip": 7200, "apps": 0} {
		if got := parts.Lists[name].Interval; got != want {
			t.Errorf("%s: interval %d, want %d", name, got, want)
		}
	}
}
