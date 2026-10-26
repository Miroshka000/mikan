package xray

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"mikan/internal/sub/model"
	"mikan/internal/sub/routing"
	"mikan/internal/sub/subtest"
)

// own is the own xray template src as the template it is.
func own(src string) model.Template { return model.Template{Core: model.CoreXray, Body: src} }

// The list made of the admin's own xray template on the two-node users, each config checked
// by xray.
func TestGoldenOwnXrayTemplate(t *testing.T) {
	for _, v := range subtest.Variants {
		if strings.HasPrefix(v.Name, "single") {
			continue // the template names the second node's group
		}
		t.Run(v.Name, func(t *testing.T) {
			t.Parallel()
			goldenXray(t, v, routing.All, own(subtest.OwnXrayTemplate), v.Name+".own.xray.json")
		})
	}
}

// The list of an own template: the balancer of the choice first, standing for proxy, then
// a config per server with the server as proxy. A config carries the servers of the
// balancers its rules name, a balancer of no servers selects the blackhole, and the
// panel's hosts go direct ahead of the admin's rules.
func TestOwnXrayTemplate(t *testing.T) {
	p := profileOf(subtest.Multi())
	cs := render(t, p, routing.Blocked, own(subtest.OwnXrayTemplate))
	outs := outboundsOf(p.Proxies)
	if len(cs) != 1+len(outs) || cs[0].Remarks != "⚡ Авто" || cs[1].Remarks != outs[0].name {
		t.Fatalf("the list: %d configs, %q %q", len(cs), cs[0].Remarks, cs[1].Remarks)
	}
	tags := func(c xrayConfig) []string {
		var out []string
		for _, o := range c.Outbounds {
			out = append(out, o["tag"].(string))
		}
		return out
	}
	balancer := func(c xrayConfig, tag string) *Balancer {
		for i := range c.Routing.Balancers {
			if c.Routing.Balancers[i].Tag == tag {
				return &c.Routing.Balancers[i]
			}
		}
		return nil
	}
	server := cs[1]
	if got := tags(server); got[0] != proxyTag || !slices.Contains(got, "block") || !slices.Contains(got, "dns-out") {
		t.Fatalf("a server's config: %v", got)
	}
	if b := balancer(server, "⚡ Авто"); b != nil {
		t.Fatalf("a balancer no rule names is carried: %+v", b)
	}
	vless := balancer(server, "🔒 VLESS")
	if vless == nil || len(vless.Selector) == 0 {
		t.Fatalf("the VLESS balancer: %+v", server.Routing.Balancers)
	}
	for _, tag := range vless.Selector {
		i := slices.IndexFunc(outs, func(o tagged) bool { return o.tag == tag })
		if i < 0 || !strings.HasPrefix(outs[i].name, "🇳🇱") || !slices.Contains(tags(server), tag) {
			t.Fatalf("the VLESS balancer selects %s, of %v", tag, tags(server))
		}
	}
	if b := balancer(server, "🇯🇵 Япония"); b == nil || !slices.Equal(b.Selector, []string{"block"}) {
		t.Fatalf("a balancer of no servers is open: %+v", b)
	}
	if r := server.Routing.Rules[0]; r["outboundTag"] != "direct" {
		t.Fatalf("the panel's hosts: %v", server.Routing.Rules)
	}
	auto := cs[0]
	last := auto.Routing.Rules[len(auto.Routing.Rules)-1]
	raw, _ := json.Marshal(auto.Routing.Rules)
	if slices.Contains(tags(auto), proxyTag) || last["balancerTag"] != "⚡ Авто" || strings.Contains(string(raw), `"outboundTag":"proxy"`) ||
		strings.Count(string(raw), `"network":"tcp,udp"`) != 1 {
		t.Fatalf("the choice's config: %v %s", tags(auto), raw)
	}
	if b := balancer(auto, "⚡ Авто"); b == nil || len(b.Selector) != len(outs) {
		t.Fatalf("the choice takes every server: %+v", b)
	}
	if Notes(p, model.GroupNames{}, routing.All, own(subtest.OwnXrayTemplate), routing.Runetfreedom) != nil {
		t.Fatal("notes of balancers the admin wrote for xray")
	}
}

// The check refuses what would break the configs, by a code and what it is about.
func TestCheckOwnXrayTemplate(t *testing.T) {
	p := profileOf(subtest.Multi())
	for _, c := range []struct {
		name, src, code, detail string
	}{
		{"not JSON", "{\n\n\"routing\": [}", "template_json", "3"},
		{"no tag", `{"outbounds": [{"protocol": "freedom"}]}`, "template_tag", "1"},
		{"the panel's tag", `{"outbounds": [{"tag": "proxy-eu", "protocol": "freedom"}]}`, "template_proxy_tag", "proxy-eu"},
		{"a tag twice", `{"outbounds": [{"tag": "a", "protocol": "freedom"}, {"tag": "a", "protocol": "blackhole"}]}`, "template_tag_dup", "a"},
		{"a balancer without a tag", `{"routing": {"balancers": [{"selector": ["proxy"]}]}}`, "template_balancer_tag", "1"},
		{"a typo in the ask", `{"routing": {"balancers": [{"tag": "b", "mikan": {"choise": true}}]}}`, "template_ask", "b"},
		{"a fallback to nowhere", `{"routing": {"balancers": [{"tag": "b", "selector": ["proxy"], "fallbackTag": "x"}]}}`, "template_detour", "b → x"},
		{"a rule to nothing", `{"routing": {"rules": [{"domain": ["x.com"]}]}}`, "template_rule_to", "1"},
		{"a rule to nowhere", `{"routing": {"rules": [{"domain": ["x.com"], "outboundTag": "x"}]}}`, "template_rule_target", "1 (x)"},
		{"a rule to no balancer", `{"routing": {"rules": [{"domain": ["x.com"], "balancerTag": "x"}]}}`, "template_rule_target", "1 (x)"},
		{"rules of another kind", `{"routing": {"rules": {}}}`, "template_shape", "routing.rules"},
	} {
		err := CheckTemplate(p, c.src)
		var te *model.TemplateError
		if !errors.As(err, &te) || te.Code != c.code || te.Detail != c.detail {
			t.Errorf("%s: %v, want %s %s", c.name, err, c.code, c.detail)
		}
	}
	if err := CheckTemplate(p, subtest.OwnXrayTemplate); err != nil {
		t.Fatal(err)
	}
	p.Proxies = nil
	if err := CheckTemplate(p, subtest.OwnXrayTemplate); !errors.Is(err, model.ErrNoProxies) {
		t.Fatalf("no servers: %v", err)
	}
}

// The example to start from renders, the automatic choice ahead of the servers, and xray
// takes every config of it.
func TestXrayExample(t *testing.T) {
	g := model.GroupNames{Auto: `Быстрый "мой"`}
	cs := render(t, profileOf(subtest.Multi()), routing.All, own(string(Example(g))))
	if cs[0].Remarks != g.Auto || cs[0].Observatory == nil {
		raw, _ := json.Marshal(cs[0])
		t.Fatalf("%s", raw)
	}
}
