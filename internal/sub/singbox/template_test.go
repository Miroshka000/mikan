package singbox

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"mikan/internal/sub/model"
	"mikan/internal/sub/routing"
	"mikan/internal/sub/subtest"
)

// own is the own sing-box template src as the template it is.
func own(src string) model.Template { return model.Template{Core: model.CoreSingBox, Body: src} }

// The config made of the admin's own sing-box template on the two-node users.
func TestGoldenOwnTemplate(t *testing.T) {
	for _, v := range subtest.Variants {
		if strings.HasPrefix(v.Name, "single") {
			continue // the template names the second node's group
		}
		t.Run(v.Name, func(t *testing.T) {
			raw, err := Render(context.Background(), goldenProfile(v), v.Groups, routing.All, own(subtest.OwnSingBoxTemplate))
			if err != nil {
				t.Fatal(err)
			}
			subtest.Golden(t, v.Name+".own.singbox.json", raw)
		})
	}
}

// An own template is the config as the admin wrote it, with the servers in it: the groups
// that ask get them after their own members, one left with none gets the outbound that
// refuses, the panel's hosts go direct after the sniff and the DNS hijack, and the panel's
// routing is not in it.
func TestOwnTemplate(t *testing.T) {
	prof := profileOf(subtest.Multi())
	c := renderSingBox(t, prof, routing.Blocked, own(subtest.OwnSingBoxTemplate))
	members := func(tag string) []string {
		t.Helper()
		o := outbound(c, tag)
		if o == nil {
			t.Fatalf("no %s: %v", tag, c.Outbounds)
		}
		var out []string
		for _, m := range o["outbounds"].([]any) {
			out = append(out, m.(string))
		}
		return out
	}
	servers := []string{}
	for _, x := range prof.Proxies {
		if outbound(c, x.Name) != nil {
			servers = append(servers, x.Name)
		}
	}
	if len(servers) == 0 || !slices.Equal(members("⚡ Авто"), servers) || outbound(c, "⚡ Авто")["mikan"] != nil {
		t.Fatalf("every server: %v of %v", members("⚡ Авто"), servers)
	}
	for _, m := range members("🇺🇸 США") {
		if !strings.HasPrefix(m, "🇺🇸") {
			t.Fatalf("the filter: %v", members("🇺🇸 США"))
		}
	}
	if got := members("🇯🇵 Япония"); !slices.Equal(got, []string{rejectTag}) || outbound(c, rejectTag)["type"] != "socks" {
		t.Fatalf("a group of no servers is open: %v %v", got, outbound(c, rejectTag))
	}
	if got := members("🌍 Сеть"); !slices.Equal(got, []string{"⚡ Авто", "🇺🇸 США", "🔒 VLESS", "🇯🇵 Япония", "direct"}) {
		t.Fatalf("a group of its own members: %v", got)
	}
	if len(c.Route.Rules) < 3 || c.Route.Rules[1]["action"] != "hijack-dns" || c.Route.Rules[2]["outbound"] != "direct" ||
		!slices.Contains(anyList(c.Route.Rules[2]["ip_cidr"]), any("203.0.113.7/32")) {
		t.Fatalf("the panel's hosts: %v", c.Route.Rules)
	}
	raw, _ := json.Marshal(c.Route.Rules)
	if strings.Contains(string(raw), "list-mikan-blocked") || c.Route.Final != "🌍 Сеть" || c.DNS.Final != "remote" {
		t.Fatalf("the panel's routing in an own template: %s", raw)
	}
	if Notes(prof, model.GroupNames{}, routing.All, own(subtest.OwnSingBoxTemplate)) != nil {
		t.Fatal("notes of groups the admin wrote for sing-box")
	}
}

// What a template has its own stays its own: an outbound of a server's name, a direct
// outbound of another tag, the REJECT it defines.
func TestOwnTemplateKeepsItsOwn(t *testing.T) {
	prof := profileOf(subtest.Multi())
	name := prof.Proxies[0].Name
	src := `{"outbounds": [
	  {"type": "selector", "tag": "VPN", "mikan": {"types": ["hysteria9"]}},
	  {"type": "direct", "tag": "out"},
	  {"type": "http", "tag": ` + quote(name) + `, "server": "10.0.0.1", "server_port": 8080},
	  {"type": "direct", "tag": "REJECT"}
	]}`
	c := renderSingBox(t, prof, routing.All, own(src))
	if o := outbound(c, name); o["type"] != "http" {
		t.Fatalf("the template's own outbound of a server's name: %v", o)
	}
	if outbound(c, "direct") != nil || c.Route.Rules[0]["outbound"] != "out" {
		t.Fatalf("a direct outbound of its own: %v %v", c.Outbounds, c.Route.Rules)
	}
	if o := outbound(c, rejectTag); o["type"] != "direct" {
		t.Fatalf("the template's own REJECT: %v", o)
	}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// The check refuses what would break the config, by a code and what it is about; a user
// with nothing sing-box carries has no config at all.
func TestCheckOwnTemplate(t *testing.T) {
	prof := profileOf(subtest.Multi())
	for _, c := range []struct {
		name, src, code, detail string
	}{
		{"not JSON", "{\n\"outbounds\": [}", "template_json", "2"},
		{"a list", "[]", "template_object", ""},
		{"outbounds of another kind", `{"outbounds": {}}`, "template_shape", "outbounds"},
		{"no tag", `{"outbounds": [{"type": "direct"}]}`, "template_tag", "1"},
		{"a tag twice", `{"outbounds": [{"type": "direct", "tag": "a"}, {"type": "block", "tag": "a"}]}`, "template_tag_dup", "a"},
		{"an ask on no group", `{"outbounds": [{"type": "direct", "tag": "a", "mikan": {}}]}`, "template_ask", "a"},
		{"a typo in the ask", `{"outbounds": [{"type": "selector", "tag": "a", "mikan": {"node": ["1"]}}]}`, "template_ask", "a"},
		{"a filter that is no pattern", `{"outbounds": [{"type": "urltest", "tag": "a", "mikan": {"filter": "("}}]}`, "template_ask", "a"},
		{"a member nobody defines", `{"outbounds": [{"type": "selector", "tag": "a", "outbounds": ["b"]}]}`, "template_group_member", "a → b"},
		{"a group of nothing", `{"outbounds": [{"type": "selector", "tag": "a"}]}`, "template_group_empty", "a"},
		{"a rule to nowhere", `{"route": {"rules": [{"domain": ["x.com"], "outbound": "b"}]}}`, "template_rule_target", "1 (b)"},
		{"a rule set nobody defines", `{"route": {"rules": [{"rule_set": ["b"], "outbound": "direct"}]}}`, "template_rule_set", "1 (b)"},
		{"in a logical rule too", `{"route": {"rules": [{"type": "logical", "mode": "and", "rules": [{"rule_set": "c"}], "outbound": "direct"}]}}`, "template_rule_set", "1 (c)"},
		{"a final to nowhere", `{"route": {"final": "b"}}`, "template_final", "b"},
		{"a detour to nowhere", `{"dns": {"servers": [{"type": "https", "tag": "r", "server": "1.1.1.1", "detour": "b"}]}}`, "template_detour", "r → b"},
		{"rules of another kind", `{"route": {"rules": {}}}`, "template_shape", "route.rules"},
	} {
		err := CheckTemplate(prof, c.src)
		var te *model.TemplateError
		if !errors.As(err, &te) || te.Code != c.code || te.Detail != c.detail {
			t.Errorf("%s: %v, want %s %s", c.name, err, c.code, c.detail)
		}
	}
	if err := CheckTemplate(prof, subtest.OwnSingBoxTemplate); err != nil {
		t.Fatal(err)
	}
	prof.Proxies = nil
	if err := CheckTemplate(prof, subtest.OwnSingBoxTemplate); !errors.Is(err, model.ErrNoProxies) {
		t.Fatalf("no servers: %v", err)
	}
}

// The example to start from is a template that renders, with the panel's group names.
func TestExample(t *testing.T) {
	g := model.GroupNames{Main: `Прокси "мой"`, Auto: "Fast"}
	src := string(Example(g))
	c := renderSingBox(t, profileOf(subtest.TwoNodes()), routing.All, own(src))
	if c.Route.Final != g.Main || outbound(c, g.Auto) == nil || len(anyList(outbound(c, g.Main)["outbounds"])) < 2 {
		t.Fatalf("%s", src)
	}
}
