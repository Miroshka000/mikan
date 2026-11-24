package serve

import (
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"mikan/internal/sub/apps"
	"mikan/internal/sub/model"
	"mikan/internal/sub/routing"
	"mikan/internal/sub/xray"
)

// simpleRouting is a routing apart from the settings' default in every part a profile shows:
// the mode, a service, the DNS, a fine switch and a rule of the admin's.
func simpleRouting(t *testing.T) Routing {
	t.Helper()
	rules, err := routing.ParseRules("DOMAIN-SUFFIX,simple.example,DIRECT\n# a comment\nGEOSITE,category-ads-all,REJECT", model.GroupNames{}.WithDefaults("en"))
	if err != nil {
		t.Fatal(err)
	}
	return Routing{Mode: routing.Blocked, Rules: rules, Routes: routing.Routes{
		Services: map[string]string{"youtube": routing.TargetDirect, "telegram": routing.TargetVPN},
		DNS:      routing.DNS{Nameserver: []string{"https://9.9.9.9/dns-query"}},
		Tune:     routing.Tune{BlockQUIC: true},
	}}
}

// A simple template is the built-in profile of its own routing, whatever the settings' is: the
// same bytes for a mihomo app, Stash, sing-box and xray, and the same Happ routing, as the
// settings with that routing give; and the answer says it is made of the template.
func TestSimpleTemplateIsTheBuiltInProfileOfItsRouting(t *testing.T) {
	r := simpleRouting(t)
	for _, c := range []struct{ name, target, ua string }{
		{"a mihomo app", "/" + testToken, "mihomo/1.19.31"},
		{"Stash", "/" + testToken, "Stash/3.1.1 Clash/1.9.0"},
		{"sing-box", "/" + testToken + "?format=singbox", "SFA/1.12.4"},
		{"xray", "/" + testToken + "?format=xray", "curl/8.9"},
		{"Happ", "/" + testToken + "?format=xray", "Happ/3.4.1"},
	} {
		settings := newSub(t, apps.Policy{NoBuiltin: true})
		settings.cfg.Happ.Routing = xray.HappRoutingAuto
		settings.cfg.Mode, settings.cfg.Routes, settings.cfg.Rules = r.Mode, r.Routes, r.Rules
		want := fetchAs(t, settings, http.MethodGet, c.target, c.ua)

		simple := newSub(t, apps.Policy{NoBuiltin: true})
		simple.cfg.Happ.Routing = xray.HappRoutingAuto
		simple.cfg.Templates = []model.Template{{ID: 9, Core: model.CoreMihomo, Simple: true, Default: true}}
		simple.cfg.Routings = map[int64]Routing{9: r}
		got := fetchAs(t, simple, http.MethodGet, c.target, c.ua)
		if want.Code != http.StatusOK || got.Code != want.Code || got.Body.String() != want.Body.String() {
			t.Errorf("%s: %d, not the built-in profile of the routing (%d):\n%.600s\nwant:\n%.600s", c.name, got.Code, want.Code, got.Body, want.Body)
		}
		if !maps.EqualFunc(got.Header(), want.Header(), slices.Equal) {
			t.Errorf("%s: headers %v, want %v", c.name, got.Header(), want.Header())
		}
		// The settings' own routing is not what went out.
		if plain := fetchAs(t, newSub(t, apps.Policy{NoBuiltin: true}), http.MethodGet, c.target, c.ua); plain.Body.String() == got.Body.String() {
			t.Errorf("%s: the profile of the settings' routing", c.name)
		}
		p := previewOf(simple, c.target, c.ua)
		if p.Template.ID != 9 || !p.Template.Simple || p.TemplateFailed {
			t.Errorf("%s: the preview tells of %+v (failed %v)", c.name, p.Template, p.TemplateFailed)
		}
	}
}

// sing-box and xray with a default of their own take it over a simple Clash default, and a
// rule that names a simple template gives it to the apps of every core.
func TestSimpleTemplateAndTheCoresOwn(t *testing.T) {
	r := simpleRouting(t)
	sub := newSub(t, apps.Policy{NoBuiltin: true, Rules: []apps.Rule{rule("koala", "koala", apps.Action{Template: 9})}})
	sub.cfg.Templates = []model.Template{{ID: 9, Core: model.CoreMihomo, Simple: true},
		{ID: 3, Core: model.CoreSingBox, Default: true, Body: `{"route": {"rules": [{"domain_suffix": ["own.example"], "outbound": "direct"}]}}`}}
	sub.cfg.Routings = map[int64]Routing{9: r}
	if w := fetchAs(t, sub, http.MethodGet, "/"+testToken+"?format=singbox", "SFA/1.12.4"); !strings.Contains(w.Body.String(), "own.example") {
		t.Fatalf("sing-box's own default:\n%.600s", w.Body)
	}
	// The rule names the simple one: Clash and xray take it, sing-box keeps its own default.
	for _, target := range []string{"/" + testToken, "/" + testToken + "?format=xray"} {
		if w := fetchAs(t, sub, http.MethodGet, target, "Koala-Clash/1.1.0"); !strings.Contains(w.Body.String(), "simple.example") {
			t.Errorf("%s: not the simple template the rule names:\n%.600s", target, w.Body)
		}
	}
	if w := fetchAs(t, sub, http.MethodGet, "/"+testToken+"?format=singbox", "Koala-Clash/1.1.0"); !strings.Contains(w.Body.String(), "own.example") {
		t.Errorf("sing-box takes a Clash template only without its own:\n%.600s", w.Body)
	}
}

// Stash takes a simple template, which is a built-in profile, and the zero one in place of a
// template of text; Routed lays only a simple template's routing over the settings', and gives
// the renderers the zero template for it.
func TestSimpleTemplateForStash(t *testing.T) {
	simple := model.Template{ID: 9, Core: model.CoreMihomo, Simple: true, Default: true}
	stash := apps.App{Family: apps.FamilyStash}
	if got := ProfileTemplate([]model.Template{simple}, FormatClash, 0, stash); got != simple {
		t.Errorf("Stash: %+v, want the simple template", got)
	}
	text := model.Template{ID: 1, Core: model.CoreMihomo, Body: "a", Default: true}
	if got := ProfileTemplate([]model.Template{text}, FormatClash, 0, stash); got != (model.Template{}) {
		t.Errorf("Stash with a template of text: %+v", got)
	}
	r := simpleRouting(t)
	cfg := Config{Mode: routing.All, Routings: map[int64]Routing{9: r}}
	if got, tpl := cfg.Routed(simple); got.Mode != r.Mode || len(got.Rules) != len(r.Rules) || got.Routes.Tune != r.Routes.Tune || tpl != (model.Template{}) {
		t.Errorf("a simple template: %+v, %+v", got, tpl)
	}
	if got, tpl := cfg.Routed(text); got.Mode != routing.All || got.Rules != nil || tpl != text {
		t.Errorf("a template of text keeps the settings: %+v, %+v", got, tpl)
	}
	if got, tpl := cfg.Routed(model.Template{ID: 7, Simple: true}); got.Mode != routing.All || tpl != (model.Template{}) {
		t.Errorf("a simple template with no routing keeps the settings: %+v, %+v", got, tpl)
	}
}
