package app

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/subsrc"
	"mikan/internal/sub/ready"
)

// The subscription templates over the API: a Clash one started from the built-in profile,
// refused with the rule that points nowhere and served to the mihomo apps as the default
// (and, read their way, to sing-box's); a sing-box one of the example taking sing-box's
// place; an xray one a response rule names, which the tester shows and which cannot go
// while the rule names it; and the built-in profile again once the default is off.
func TestSubTemplatesOverHTTP(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]any{settings.KeyPublicHost: "203.0.113.10", settings.KeyPanelPort: 21355} {
		if err := settings.Set(ctx, settings.New(h.st.Q), k, v); err != nil {
			t.Fatal(err)
		}
	}
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	api := "/" + adminPath + "/api/v1"
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	preview := func(core, body string, starter bool) (int, string, []byte) {
		t.Helper()
		resp, raw := h.do(http.MethodPost, api+"/sub-templates/preview", map[string]any{"core": core, "body": body, "starter": starter}, csrf)
		var pv struct {
			Profile string `json:"profile"`
		}
		_ = json.Unmarshal(raw, &pv)
		return resp.StatusCode, pv.Profile, raw
	}
	type template struct {
		ID      int64  `json:"id"`
		Default bool   `json:"default"`
		Body    string `json:"body"`
	}
	create := func(core, name, body string, def bool) template {
		t.Helper()
		resp, raw := h.do(http.MethodPost, api+"/sub-templates", map[string]any{"core": core, "name": name, "body": body, "default": def}, csrf)
		var out template
		if resp.StatusCode != http.StatusCreated || json.Unmarshal(raw, &out) != nil || out.ID == 0 || out.Default != def {
			t.Fatalf("create %s: %d %s", name, resp.StatusCode, raw)
		}
		return out
	}

	code, starter, raw := preview("mihomo", "", true)
	if code != http.StatusOK || !strings.Contains(starter, "include-all-proxies: true") || strings.Contains(starter, "uuid") {
		t.Fatalf("starter: %d %s", code, raw)
	}
	own := strings.Replace(starter+"\n# mine\n", "rules:\n", "rules:\n  - DOMAIN-SUFFIX,mine.example,DIRECT\n", 1)
	bad := strings.Replace(own, "MATCH,VPN", "MATCH,Nowhere", 1)
	resp, raw := h.do(http.MethodPost, api+"/sub-templates", map[string]any{"core": "mihomo", "name": "Bad", "body": bad}, csrf)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(raw), "template_rule_target") || !strings.Contains(string(raw), "Nowhere") {
		t.Fatalf("bad template: %d %s", resp.StatusCode, raw)
	}
	if code, _, raw := preview("mihomo", bad, false); code != http.StatusUnprocessableEntity || !strings.Contains(string(raw), "template_rule_target") {
		t.Fatalf("bad preview: %d %s", code, raw)
	}
	clash := create("mihomo", "Clash", own, true)

	// A change of the text is checked as a new template is, and a good one is kept.
	at := api + "/sub-templates/" + strconv.FormatInt(clash.ID, 10)
	if resp, raw := h.do(http.MethodPatch, at, map[string]any{"body": bad}, csrf); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(raw), "template_rule_target") {
		t.Fatalf("bad change: %d %s", resp.StatusCode, raw)
	}
	edited := own + "# edited\n"
	resp, raw = h.do(http.MethodPatch, at, map[string]any{"body": edited}, csrf)
	var changed template
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &changed) != nil || changed.Body != edited || !changed.Default {
		t.Fatalf("change: %d %s", resp.StatusCode, raw)
	}
	own = edited

	clock := func() time.Time { return h.now }
	tariffs, _ := h.st.Q.ListTariffs(ctx)
	u, err := domain.NewUsers(h.st, domain.NewPool(h.st, clock), noChanges{}, clock).Create(ctx, domain.CreateInput{Name: "a", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	fetch := func(ua, query string) string {
		t.Helper()
		h.now = h.now.Add(time.Minute) // past the profile cache
		resp, body := h.do(http.MethodGet, "/"+subPath+"/"+u.SubToken+query, nil, map[string]string{"User-Agent": ua})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d %s", ua, resp.StatusCode, body)
		}
		return string(body)
	}
	if p := fetch("mihomo/1.19.31", ""); !strings.Contains(p, "DOMAIN-SUFFIX,mine.example,DIRECT") || strings.Contains(p, "include-all-proxies") || !strings.Contains(p, "IP-CIDR,203.0.113.10/32,DIRECT") {
		t.Fatalf("mihomo app:\n%s", p)
	}
	if p := fetch("Stash/3.1.1 Clash/1.9.0", ""); strings.Contains(p, "mine.example") {
		t.Fatalf("Stash gets the built-in profile:\n%s", p)
	}
	// The tester names the template as the subscription picks it: the default for a mihomo
	// app, none for Stash, which takes no own profile.
	tested := func(ua string) string {
		t.Helper()
		resp, raw := h.do(http.MethodPost, api+"/sub-rules/test", map[string]any{"user_agent": ua, "headers": []any{}}, csrf)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("the tester, %s: %d %s", ua, resp.StatusCode, raw)
		}
		return string(raw)
	}
	if raw := tested("mihomo/1.19.31"); !strings.Contains(raw, `"template":`+strconv.FormatInt(clash.ID, 10)) {
		t.Fatalf("the tester, a mihomo app: %s", raw)
	}
	if raw := tested("Stash/3.1.1 Clash/1.9.0"); !strings.Contains(raw, `"template":0`) {
		t.Fatalf("the tester, Stash: %s", raw)
	}
	if p := fetch("curl/8.9", "?format=singbox"); !strings.Contains(p, "mine.example") {
		t.Fatalf("sing-box reads the Clash default without one of its own:\n%s", p)
	}

	_, example, _ := preview("singbox", "", true)
	if code, p, raw := preview("singbox", example, false); code != http.StatusOK || !strings.Contains(p, `"tun-in"`) || strings.Contains(p, `"mikan"`) {
		t.Fatalf("the sing-box example: %d %s", code, raw)
	}
	if code, _, raw := preview("singbox", `{"route": {"final": "nowhere"}}`, false); code != http.StatusUnprocessableEntity || !strings.Contains(string(raw), "template_final") {
		t.Fatalf("a bad sing-box template: %d %s", code, raw)
	}
	create("singbox", "sing-box", example, true)
	if p := fetch("curl/8.9", "?format=singbox"); strings.Contains(p, "mine.example") || !strings.Contains(p, `"tun-in"`) {
		t.Fatalf("sing-box gets its own:\n%s", p)
	}

	_, xrayExample, _ := preview("xray", "", true)
	xr := create("xray", "Happ", xrayExample, false)
	rules := map[string]any{"builtin": true, "rules": []map[string]any{{"name": "happ", "enabled": true,
		"when": map[string]any{"op": "and", "conds": []map[string]any{{"header": "User-Agent", "op": "contains", "value": "happ", "not": false}}, "groups": []any{}},
		"do":   map[string]any{"respond": "serve", "format": "xray", "template": xr.ID}}}}
	if resp, raw := h.do(http.MethodPut, api+"/sub-rules", rules, csrf); resp.StatusCode != http.StatusOK {
		t.Fatalf("a rule naming the template: %d %s", resp.StatusCode, raw)
	}
	var list []struct {
		Remarks string `json:"remarks"`
	}
	if p := fetch("Happ/3.4.1", ""); json.Unmarshal([]byte(p), &list) != nil || len(list) < 2 || list[0].Remarks == "" || strings.Contains(p, `"mikan"`) {
		t.Fatalf("Happ gets the template the rule names:\n%.400s", p)
	}
	resp, raw = h.do(http.MethodPost, api+"/sub-rules/test", map[string]any{"user_agent": "Happ/3.4.1", "headers": []any{}}, csrf)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"template":`+strconv.FormatInt(xr.ID, 10)) {
		t.Fatalf("the tester: %d %s", resp.StatusCode, raw)
	}
	resp, raw = h.do(http.MethodDelete, api+"/sub-templates/"+strconv.FormatInt(xr.ID, 10), nil, csrf)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(raw), "template_in_use") || !strings.Contains(string(raw), "happ") {
		t.Fatalf("a template a rule names is deleted: %d %s", resp.StatusCode, raw)
	}

	resp, raw = h.do(http.MethodPatch, api+"/sub-templates/"+strconv.FormatInt(clash.ID, 10), map[string]any{"default": false}, csrf)
	var off template
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &off) != nil || off.Default || off.Body != own {
		t.Fatalf("default off: %d %s", resp.StatusCode, raw)
	}
	if p := fetch("mihomo/1.19.31", ""); strings.Contains(p, "mine.example") {
		t.Fatalf("no default, still the template:\n%s", p)
	}
	if resp, raw := h.do(http.MethodDelete, api+"/sub-templates/"+strconv.FormatInt(clash.ID, 10), nil, csrf); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d %s", resp.StatusCode, raw)
	}
	resp, raw = h.do(http.MethodGet, api+"/sub-templates", nil, nil)
	if resp.StatusCode != http.StatusOK || strings.Contains(string(raw), `"Clash"`) || !strings.Contains(string(raw), `"rules":["happ"]`) {
		t.Fatalf("the list: %d %s", resp.StatusCode, raw)
	}

	// A core holds at most subsrc.MaxTemplates templates: the next one is refused.
	var heads struct {
		Templates []struct {
			Core string `json:"core"`
		} `json:"templates"`
	}
	if err := json.Unmarshal(raw, &heads); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, x := range heads.Templates {
		if x.Core == "mihomo" {
			n++
		}
	}
	for i := n; i < subsrc.MaxTemplates; i++ {
		create("mihomo", "Clash "+strconv.Itoa(i), own, false)
	}
	resp, raw = h.do(http.MethodPost, api+"/sub-templates", map[string]any{"core": "mihomo", "name": "One too many", "body": own}, csrf)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(raw), "template_limit") {
		t.Fatalf("over the limit: %d %s", resp.StatusCode, raw)
	}
}

// A template is at most subsrc.MaxTemplateSize bytes, not runes: a text in Cyrillic under the
// limit in runes is refused on create, change and preview, and an ASCII one of exactly the
// limit is kept.
func TestSubTemplateSizeInBytes(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]any{settings.KeyPublicHost: "203.0.113.10", settings.KeyPanelPort: 21355} {
		if err := settings.Set(ctx, settings.New(h.st.Q), k, v); err != nil {
			t.Fatal(err)
		}
	}
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	api := "/" + adminPath + "/api/v1"
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	resp, raw := h.do(http.MethodPost, api+"/sub-templates/preview", map[string]any{"core": "mihomo", "starter": true}, csrf)
	var pv struct {
		Profile string `json:"profile"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &pv) != nil || pv.Profile == "" {
		t.Fatalf("starter: %d %s", resp.StatusCode, raw)
	}
	// The starter padded with a comment to n bytes.
	padded := func(n int) string {
		return pv.Profile + "#" + strings.Repeat("x", n-len(pv.Profile)-2) + "\n"
	}
	cyrillic := pv.Profile + "#" + strings.Repeat("я", 300<<10) + "\n"
	refused := func(what string, resp *http.Response, raw []byte) {
		t.Helper()
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(raw), `"template_size"`) || !strings.Contains(string(raw), strconv.Itoa(subsrc.MaxTemplateSize)) {
			t.Fatalf("%s: %d %.300s", what, resp.StatusCode, raw)
		}
	}

	resp, raw = h.do(http.MethodPost, api+"/sub-templates", map[string]any{"core": "mihomo", "name": "Cyrillic", "body": cyrillic}, csrf)
	refused("create in Cyrillic", resp, raw)
	resp, raw = h.do(http.MethodPost, api+"/sub-templates/preview", map[string]any{"core": "mihomo", "body": cyrillic}, csrf)
	refused("preview in Cyrillic", resp, raw)
	// Exactly the limit in runes and one byte over it: maxLength lets it through, the bytes do
	// not. A comment alone: the starter has letters of more than a byte of its own.
	edge := "#я" + strings.Repeat("x", subsrc.MaxTemplateSize-2)
	if len(edge) != subsrc.MaxTemplateSize+1 || utf8.RuneCountInString(edge) != subsrc.MaxTemplateSize {
		t.Fatalf("the edge is %d bytes, %d runes", len(edge), utf8.RuneCountInString(edge))
	}
	resp, raw = h.do(http.MethodPost, api+"/sub-templates", map[string]any{"core": "mihomo", "name": "Edge", "body": edge}, csrf)
	refused("a byte over the limit in runes", resp, raw)

	full := padded(subsrc.MaxTemplateSize)
	resp, raw = h.do(http.MethodPost, api+"/sub-templates", map[string]any{"core": "mihomo", "name": "Full", "body": full}, csrf)
	var kept struct {
		ID   int64 `json:"id"`
		Size int64 `json:"size"`
	}
	if resp.StatusCode != http.StatusCreated || json.Unmarshal(raw, &kept) != nil || kept.Size != subsrc.MaxTemplateSize {
		t.Fatalf("exactly the limit: %d %.300s", resp.StatusCode, raw)
	}
	at := api + "/sub-templates/" + strconv.FormatInt(kept.ID, 10)
	resp, raw = h.do(http.MethodPatch, at, map[string]any{"body": cyrillic}, csrf)
	refused("change to Cyrillic", resp, raw)
	if resp, raw := h.do(http.MethodPatch, at, map[string]any{"body": full + " "}, csrf); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a byte over the limit: %d %.300s", resp.StatusCode, raw)
	}
	if resp, raw := h.do(http.MethodPost, api+"/sub-templates", map[string]any{"core": "mihomo", "name": "Over", "body": full + " "}, csrf); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a byte over the limit: %d %.300s", resp.StatusCode, raw)
	}
}

// The ready templates over the API: the catalog by core with its authors and licenses, the
// text of each, mikan's own made as the editor's starter is, each saved as it is loaded, and
// none of them in the database until it is.
func TestReadySubTemplatesOverHTTP(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]any{settings.KeyPublicHost: "203.0.113.10", settings.KeyPanelPort: 21355} {
		if err := settings.Set(ctx, settings.New(h.st.Q), k, v); err != nil {
			t.Fatal(err)
		}
	}
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	api := "/" + adminPath + "/api/v1"
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	type item struct {
		ID      string `json:"id"`
		Core    string `json:"core"`
		Author  string `json:"author"`
		License string `json:"license"`
		Source  string `json:"source"`
		Builtin bool   `json:"builtin"`
		Body    string `json:"body"`
	}
	resp, raw := h.do(http.MethodGet, api+"/sub-templates/ready", nil, nil)
	var list struct {
		Templates []item `json:"templates"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &list) != nil || len(list.Templates) != len(ready.All()) {
		t.Fatalf("list: %d %s", resp.StatusCode, raw)
	}
	for i, x := range list.Templates {
		r := ready.All()[i]
		if x.ID != r.ID || x.Core != string(r.Core) || x.Author != r.Author || x.License != r.License || x.Source != r.Source || x.Builtin != r.Starter || x.Body != "" {
			t.Errorf("list: %+v, want %s", x, r.ID)
		}
	}
	if resp, raw := h.do(http.MethodGet, api+"/sub-templates/ready/nope", nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown: %d %s", resp.StatusCode, raw)
	}
	texts := map[string]string{}
	for _, r := range ready.All() {
		resp, raw := h.do(http.MethodGet, api+"/sub-templates/ready/"+r.ID, nil, nil)
		var x item
		if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &x) != nil || x.ID != r.ID || x.Body == "" {
			t.Fatalf("%s: %d %.300s", r.ID, resp.StatusCode, raw)
		}
		want := r.Body
		if r.Starter {
			resp, raw := h.do(http.MethodPost, api+"/sub-templates/preview", map[string]any{"core": r.Core, "starter": true}, csrf)
			var pv struct {
				Profile string `json:"profile"`
			}
			if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &pv) != nil {
				t.Fatalf("starter %s: %d %s", r.Core, resp.StatusCode, raw)
			}
			want = pv.Profile
		}
		if x.Body != want {
			t.Errorf("%s: the text is not the catalog's:\n%s", r.ID, x.Body)
		}
		texts[r.ID] = x.Body
	}
	if heads, err := h.st.Q.ListSubTemplateHeads(ctx); err != nil || len(heads) != 0 {
		t.Fatalf("loading saved templates: %v %v", heads, err)
	}
	// Each is a template the panel saves as it is loaded.
	for _, r := range ready.All() {
		if resp, raw := h.do(http.MethodPost, api+"/sub-templates", map[string]any{"core": r.Core, "name": r.ID, "body": texts[r.ID]}, csrf); resp.StatusCode != http.StatusCreated {
			t.Errorf("save %s: %d %.300s", r.ID, resp.StatusCode, raw)
		}
	}
}
