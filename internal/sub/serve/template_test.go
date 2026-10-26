package serve

import (
	"net/http"
	"strings"
	"testing"

	"mikan/internal/sub/apps"
	"mikan/internal/sub/model"
	"mikan/internal/sub/subtest"
)

// A core's template is the one the rule names when it is the core's, else the core's
// default; sing-box and xray with none of their own read the Clash one picked the same
// way, and the links have none.
func TestTemplateSelection(t *testing.T) {
	clash := model.Template{ID: 1, Core: model.CoreMihomo, Body: "a", Default: true}
	clash2 := model.Template{ID: 2, Core: model.CoreMihomo, Body: "b"}
	sb := model.Template{ID: 3, Core: model.CoreSingBox, Body: "c", Default: true}
	xr := model.Template{ID: 4, Core: model.CoreXray, Body: "d"}
	ts := []model.Template{clash, clash2, sb, xr}
	for _, c := range []struct {
		name   string
		format string
		named  int64
		want   model.Template
	}{
		{"the default", FormatClash, 0, clash},
		{"the one named", FormatClash, clash2.ID, clash2},
		{"named of another core", FormatClash, sb.ID, clash},
		{"a template that is gone", FormatClash, 99, clash},
		{"sing-box's own default", FormatSingBox, 0, sb},
		{"sing-box's own over a Clash one named", FormatSingBox, clash2.ID, sb},
		{"xray without a default reads the Clash default", FormatXray, 0, clash},
		{"xray's own named", FormatXray, xr.ID, xr},
		{"xray reads the Clash one named", FormatXray, clash2.ID, clash2},
		{"the links", FormatURI, xr.ID, model.Template{}},
	} {
		if got := Template(ts, c.format, c.named); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
	if got := Template([]model.Template{clash2}, FormatSingBox, 0); got != (model.Template{}) {
		t.Errorf("no default anywhere: %+v", got)
	}
}

// The profile is made of the template the deciding rule names, and of the core's default
// for every other request.
func TestRuleNamesTheTemplate(t *testing.T) {
	sub := newSub(t, apps.Policy{NoBuiltin: true, Rules: []apps.Rule{rule("koala", "koala", apps.Action{Template: 2})}})
	clash := func(id int64, site string, def bool) model.Template {
		return model.Template{ID: id, Core: model.CoreMihomo, Default: def,
			Body: "proxy-groups:\n  - {name: Net, type: select, include-all-proxies: true}\nrules:\n  - DOMAIN-SUFFIX," + site + ",DIRECT\n  - MATCH,Net\n"}
	}
	sub.cfg.Templates = []model.Template{clash(1, "default.example", true), clash(2, "koala.example", false),
		{ID: 3, Core: model.CoreSingBox, Default: true, Body: subtest.OwnSingBoxTemplate}}
	if w := fetch(t, sub, "mihomo/1.19.31"); !strings.Contains(w.Body.String(), "default.example") {
		t.Fatalf("the default:\n%s", w.Body)
	}
	if w := fetch(t, sub, "Koala-Clash/1.1.0"); !strings.Contains(w.Body.String(), "koala.example") || strings.Contains(w.Body.String(), "default.example") {
		t.Fatalf("the one the rule names:\n%s", w.Body)
	}
	w := fetchAs(t, sub, http.MethodGet, "/"+testToken+"?format=singbox", "Koala-Clash/1.1.0")
	if !strings.Contains(w.Body.String(), "geosite-ru") || strings.Contains(w.Body.String(), "koala.example") {
		t.Fatalf("sing-box gets its own default:\n%s", w.Body)
	}
}

// A saved template that does not render for this user leaves the app the built-in profile,
// and says so once.
func TestTemplateThatDoesNotRender(t *testing.T) {
	sub := newSub(t, apps.Policy{NoBuiltin: true})
	// The default user has no server of a US node: a group that names one by hand is empty.
	sub.cfg.Templates = []model.Template{{ID: 5, Core: model.CoreSingBox, Default: true,
		Body: `{"outbounds": [{"type": "selector", "tag": "VPN", "outbounds": ["🇺🇸 VLESS Vision"]}]}`}}
	w := fetchAs(t, sub, http.MethodGet, "/"+testToken+"?format=singbox", "curl/8.9")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"final"`) || strings.Contains(w.Body.String(), "🇺🇸") {
		t.Fatalf("%d\n%s", w.Code, w.Body)
	}
}
