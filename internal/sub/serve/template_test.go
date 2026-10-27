package serve

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
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

// The profile is made of the template Template picks, but a Clash app that takes no own
// profile gets the built-in one; the other formats are Template's for every app.
func TestProfileTemplate(t *testing.T) {
	clash := model.Template{ID: 1, Core: model.CoreMihomo, Body: "a", Default: true}
	sb := model.Template{ID: 3, Core: model.CoreSingBox, Body: "c", Default: true}
	ts := []model.Template{clash, sb}
	for _, c := range []struct {
		name   string
		format string
		app    apps.App
		want   model.Template
	}{
		{"a mihomo app", FormatClash, apps.App{Family: apps.FamilyMihomo}, clash},
		{"the zero app", FormatClash, apps.App{}, clash},
		{"Stash", FormatClash, apps.App{Family: apps.FamilyStash}, model.Template{}},
		{"an app of another core told to take Clash", FormatClash, apps.App{Family: apps.FamilyXray}, model.Template{}},
		{"an app we do not know", FormatClash, apps.App{Family: apps.FamilyOther}, model.Template{}},
		{"xray without its own reads the Clash default", FormatXray, apps.App{Family: apps.FamilyXray}, clash},
		{"Stash asking for sing-box", FormatSingBox, apps.App{Family: apps.FamilyStash}, sb},
	} {
		if got := ProfileTemplate(ts, c.format, 0, c.app); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

// A saved template that does not render for this user leaves the app the built-in profile,
// and says so once; a Clash one leaves the Mikan app the groups of the built-in profile.
func TestTemplateThatDoesNotRender(t *testing.T) {
	// The default user has no server of a US node: a group that names one by hand is empty.
	for _, c := range []struct {
		core   model.Core
		target string
		body   string
	}{
		{model.CoreMihomo, "/" + testToken, "proxy-groups:\n  - {name: VPN, type: select, proxies: [\"🇺🇸 VLESS Vision\"]}\nrules:\n  - MATCH,VPN\n"},
		{model.CoreSingBox, "/" + testToken + "?format=singbox", `{"outbounds": [{"type": "selector", "tag": "VPN", "outbounds": ["🇺🇸 VLESS Vision"]}]}`},
		{model.CoreXray, "/" + testToken + "?format=xray", `{"outbounds": {"tag": "VPN"}}`},
	} {
		sub := newSub(t, apps.Policy{NoBuiltin: true})
		sub.groups = testGroups()
		serve := func() (*httptest.ResponseRecorder, *Handler, string) {
			var logged bytes.Buffer
			h := handlerOf(sub)
			h.SetLogger(slog.New(slog.NewTextHandler(&logged, nil)))
			r := httptest.NewRequest(http.MethodGet, c.target, nil)
			r.Header.Set("User-Agent", "mihomo/1.19.31")
			r.Header.Set(FeaturesHeader, "groups")
			w := httptest.NewRecorder()
			h.Serve(w, r, testToken)
			return w, h, logged.String()
		}
		builtIn, _, _ := serve()
		sub.cfg.Templates = []model.Template{{ID: 5, Core: c.core, Default: true, Body: c.body}}
		w, h, logged := serve()
		if w.Code != http.StatusOK || w.Body.String() != builtIn.Body.String() {
			t.Errorf("%s: %d, not the built-in profile:\n%.400s", c.core, w.Code, w.Body)
		}
		msg := `msg="subscription: an own template does not render, the built-in profile goes out" template=5 core=` + string(c.core)
		if n := strings.Count(logged, "level=WARN"); n != 1 || !strings.Contains(logged, msg) {
			t.Errorf("%s: %d warnings: %s", c.core, n, logged)
		}
		var keys []string
		h.logged.Range(func(k, _ any) bool { keys = append(keys, k.(string)); return true })
		if len(keys) != 1 || !strings.HasPrefix(keys[0], "template/5/template_") {
			t.Errorf("%s: warned of %v", c.core, keys)
		}
		if c.core == model.CoreMihomo && (w.Header().Get(GroupsHeader) == "" || len(sub.groupsOf) != 2 || len(sub.groupsOf[1]) == 0 ||
			!slices.EqualFunc(sub.groupsOf[1], sub.groupsOf[0], func(a, b model.Proxy) bool { return a.Name == b.Name })) {
			t.Errorf("the groups of the built-in profile: %q of %v", w.Header().Get(GroupsHeader), sub.groupsOf)
		}
	}
}
