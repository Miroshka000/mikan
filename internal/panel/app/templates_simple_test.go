package app

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
)

// A simple template over the API: refused where the settings it stands for would be, kept as a
// Clash template with its routing and no text, served to every core and to Stash in place of
// the settings' routing, and shown by the tester; the preview makes any core's profile of a
// routing and the Clash starter of one; a body makes it a template of text, and a routing a
// simple one again.
func TestSimpleSubTemplatesOverHTTP(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	set := settings.New(h.st.Q)
	for k, v := range map[string]any{settings.KeyPublicHost: "203.0.113.10", settings.KeyPanelPort: 21355, settings.KeyRules: "DOMAIN-SUFFIX,settings.example,DIRECT",
		settings.KeyGroupMain: "Main"} {
		if err := settings.Set(ctx, set, k, v); err != nil {
			t.Fatal(err)
		}
	}
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	api := "/" + adminPath + "/api/v1"
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	routingOf := func(rules string, services map[string]string) map[string]any {
		return map[string]any{"mode": "blocked", "routes": map[string]any{"services": services, "tune": map[string]any{"block_quic": true}}, "rules": rules}
	}
	good := routingOf("DOMAIN-SUFFIX,simple.example,DIRECT\n# mine\n\n", map[string]string{"youtube": "direct"})
	for _, c := range []struct {
		name               string
		body               map[string]any
		code, field, value string
	}{
		{"a server nobody has", map[string]any{"core": "mihomo", "name": "a", "routing": routingOf("", map[string]string{"youtube": "node:999"})}, "routes_target", "body.routing.routes", ""},
		{"a rule to a group nobody has", map[string]any{"core": "mihomo", "name": "a", "routing": routingOf("DOMAIN,a.example,DIRECT\nDOMAIN,b.example,Nowhere", nil)}, "rule_target", "body.routing.rules", `"value":2`},
		{"a text and a routing", map[string]any{"core": "mihomo", "name": "a", "body": "rules: []", "routing": good}, "template_body_or_routing", "body.routing", ""},
		{"a simple one of sing-box", map[string]any{"core": "singbox", "name": "a", "routing": good}, "template_simple_core", "body.core", ""},
		{"neither", map[string]any{"core": "mihomo", "name": "a"}, "template_empty", "body.body", ""},
	} {
		if resp, raw := h.do(http.MethodPost, api+"/sub-templates", c.body, csrf); resp.StatusCode != http.StatusUnprocessableEntity ||
			!strings.Contains(string(raw), `"`+c.code+`"`) || !strings.Contains(string(raw), `"location":"`+c.field+`"`) || !strings.Contains(string(raw), c.value) {
			t.Errorf("%s: %d %s", c.name, resp.StatusCode, raw)
		}
	}

	type template struct {
		ID      int64  `json:"id"`
		Kind    string `json:"kind"`
		Default bool   `json:"default"`
		Body    string `json:"body"`
		Routing *struct {
			Mode  string `json:"mode"`
			Rules string `json:"rules"`
		} `json:"routing"`
	}
	// read is the template of an answer of the status code.
	read := func(code int) func(*http.Response, []byte) template {
		return func(resp *http.Response, raw []byte) template {
			t.Helper()
			var out template
			if resp.StatusCode != code || json.Unmarshal(raw, &out) != nil {
				t.Fatalf("%d %s", resp.StatusCode, raw)
			}
			return out
		}
	}
	simple := read(http.StatusCreated)(h.do(http.MethodPost, api+"/sub-templates", map[string]any{"core": "mihomo", "name": "Simple", "routing": good, "default": true}, csrf))
	if simple.Kind != "simple" || simple.Body != "" || !simple.Default || simple.Routing == nil || simple.Routing.Mode != "blocked" ||
		simple.Routing.Rules != "DOMAIN-SUFFIX,simple.example,DIRECT\n# mine" {
		t.Fatalf("created: %+v", simple)
	}
	if resp, raw := h.do(http.MethodGet, api+"/sub-templates", nil, nil); resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"kind":"simple"`) {
		t.Fatalf("the list: %d %s", resp.StatusCode, raw)
	}

	clock := func() time.Time { return h.now }
	tariffs, _ := h.st.Q.ListTariffs(ctx)
	u, err := domain.NewUsers(h.st, domain.NewPool(h.st, clock), noChanges{}, clock).Create(ctx, domain.CreateInput{Name: "a", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	fetch := func(ua, query string) string {
		t.Helper()
		h.advance(time.Minute) // past the profile cache
		resp, body := h.do(http.MethodGet, "/"+subPath+"/"+u.SubToken+query, nil, map[string]string{"User-Agent": ua})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d %s", ua, resp.StatusCode, body)
		}
		return string(body)
	}
	for _, c := range []struct{ name, ua, query string }{
		{"a mihomo app", "mihomo/1.19.31", ""},
		{"Stash", "Stash/3.1.1 Clash/1.9.0", ""},
		{"sing-box", "curl/8.9", "?format=singbox"},
		{"xray", "curl/8.9", "?format=xray"},
	} {
		if p := fetch(c.ua, c.query); !strings.Contains(p, "simple.example") || strings.Contains(p, "settings.example") {
			t.Errorf("%s: not the simple template's routing:\n%.800s", c.name, p)
		}
	}
	tested := func(ua string) string {
		t.Helper()
		resp, raw := h.do(http.MethodPost, api+"/sub-rules/test", map[string]any{"user_agent": ua, "headers": []any{}}, csrf)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("the tester, %s: %d %s", ua, resp.StatusCode, raw)
		}
		return string(raw)
	}
	for _, ua := range []string{"mihomo/1.19.31", "Stash/3.1.1 Clash/1.9.0"} {
		if raw := tested(ua); !strings.Contains(raw, `"template":`+strconv.FormatInt(simple.ID, 10)) {
			t.Errorf("the tester, %s: %s", ua, raw)
		}
	}

	preview := func(body map[string]any) (int, string, []byte) {
		t.Helper()
		resp, raw := h.do(http.MethodPost, api+"/sub-templates/preview", body, csrf)
		var pv struct {
			Profile string `json:"profile"`
		}
		_ = json.Unmarshal(raw, &pv)
		return resp.StatusCode, pv.Profile, raw
	}
	for _, core := range []string{"mihomo", "singbox", "xray"} {
		if code, p, raw := preview(map[string]any{"core": core, "routing": good}); code != http.StatusOK || !strings.Contains(p, "simple.example") || strings.Contains(p, "settings.example") {
			t.Errorf("the preview of %s: %d %.400s", core, code, raw)
		}
	}
	code, starter, raw := preview(map[string]any{"core": "mihomo", "routing": good, "starter": true})
	if code != http.StatusOK || !strings.Contains(starter, "include-all-proxies: true") || !strings.Contains(starter, "DOMAIN-SUFFIX,simple.example,DIRECT") {
		t.Fatalf("the starter of the routing: %d %s", code, raw)
	}
	if code, _, raw := preview(map[string]any{"core": "mihomo", "routing": routingOf("", map[string]string{"youtube": "node:999"})}); code != http.StatusUnprocessableEntity || !strings.Contains(string(raw), "routes_target") {
		t.Errorf("the preview of a server nobody has: %d %s", code, raw)
	}
	if code, _, raw := preview(map[string]any{"core": "mihomo", "body": starter, "routing": good}); code != http.StatusUnprocessableEntity || !strings.Contains(string(raw), "template_body_or_routing") {
		t.Errorf("the preview of a text and a routing: %d %s", code, raw)
	}

	// "Open as text": the starter saved as the body makes it a template of text, and a routing
	// a simple one again; a change may not send both.
	at := api + "/sub-templates/" + strconv.FormatInt(simple.ID, 10)
	text := read(http.StatusOK)(h.do(http.MethodPatch, at, map[string]any{"body": starter}, csrf))
	if text.Kind != "text" || text.Body != starter || text.Routing != nil || !text.Default {
		t.Fatalf("as text: %+v", text)
	}
	if p := fetch("mihomo/1.19.31", ""); !strings.Contains(p, "simple.example") || strings.Contains(p, "include-all-proxies") {
		t.Errorf("the template of text the starter makes:\n%.800s", p)
	}
	again := read(http.StatusOK)(h.do(http.MethodPatch, at, map[string]any{"routing": good}, csrf))
	if again.Kind != "simple" || again.Body != "" || again.Routing == nil {
		t.Fatalf("simple again: %+v", again)
	}
	if resp, raw := h.do(http.MethodPatch, at, map[string]any{"body": starter, "routing": good}, csrf); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(raw), "template_body_or_routing") {
		t.Errorf("a change of both: %d %s", resp.StatusCode, raw)
	}
	// A group a simple template's rule sends to is not renamed from under it, as with the
	// settings' own rules.
	read(http.StatusCreated)(h.do(http.MethodPost, api+"/sub-templates", map[string]any{"core": "mihomo", "name": "Grouped", "routing": routingOf("DOMAIN,c.example,Main", nil)}, csrf))
	if resp, raw := h.do(http.MethodPatch, api+"/settings", map[string]any{"sub_group_main": "Other"}, csrf); resp.StatusCode != http.StatusUnprocessableEntity ||
		!strings.Contains(string(raw), "group_in_template_rules") || !strings.Contains(string(raw), "Grouped") || strings.Contains(string(raw), "Simple") {
		t.Errorf("a rename under a template's rule: %d %s", resp.StatusCode, raw)
	}
	_, example, _ := preview(map[string]any{"core": "singbox", "starter": true})
	sb := read(http.StatusCreated)(h.do(http.MethodPost, api+"/sub-templates", map[string]any{"core": "singbox", "name": "sing-box", "body": example}, csrf))
	if resp, raw := h.do(http.MethodPatch, api+"/sub-templates/"+strconv.FormatInt(sb.ID, 10), map[string]any{"routing": good}, csrf); resp.StatusCode != http.StatusUnprocessableEntity ||
		!strings.Contains(string(raw), "template_simple_core") {
		t.Errorf("a sing-box template made simple: %d %s", resp.StatusCode, raw)
	}
}
