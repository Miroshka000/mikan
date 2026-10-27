package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/store/db"
	"mikan/internal/sub/apps"
)

// jsonOf decodes an API answer.
func jsonOf(t *testing.T, resp *http.Response, body []byte, status int, v any) {
	t.Helper()
	if resp.StatusCode != status {
		t.Fatalf("%s %s: %d %s", resp.Request.Method, resp.Request.URL.Path, resp.StatusCode, body)
	}
	if v != nil {
		if err := json.Unmarshal(body, v); err != nil {
			t.Fatalf("%s: %v", body, err)
		}
	}
}

// ruleTest is the tester's answer.
type ruleTest struct {
	AppID    string `json:"app_id"`
	Rule     string `json:"rule"`
	Position int    `json:"position"`
	Respond  string `json:"respond"`
	Status   int    `json:"status"`
	Format   string `json:"format"`
	Text     string `json:"text"`
}

// stubText is the name of the placeholder entry of a stub in the format of a link list.
func stubText(t *testing.T, body []byte) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(string(body))
	if err != nil {
		t.Fatalf("not a list of links: %.100s", body)
	}
	_, frag, _ := strings.Cut(string(raw), "#")
	name, err := url.PathUnescape(strings.TrimSpace(frag))
	if err != nil {
		t.Fatal(err)
	}
	return name
}

// The admin's rules, the list of blocked apps and the log, through the panel as it runs: a
// change is in the next fetch, every fetch is in the log of its user, and the retention
// is the maintenance's.
func TestSubscriptionResponseRulesEndToEnd(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	sub := subPageUser(t, h)
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	api := "/" + adminPath + "/api/v1"
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	agent := func(ua string) map[string]string { return map[string]string{"User-Agent": ua} }
	u, err := h.st.Q.GetUserBySubToken(ctx, sub[strings.LastIndex(sub, "/")+1:])
	if err != nil {
		t.Fatal(err)
	}

	// Nothing set: the built-in rules are on and the admin has none.
	var view struct {
		Rules   []apps.Rule `json:"rules"`
		Builtin bool        `json:"builtin"`
		Formats []string    `json:"formats"`
	}
	resp, out := h.do(http.MethodGet, api+"/sub-rules", nil, nil)
	jsonOf(t, resp, out, http.StatusOK, &view)
	if len(view.Rules) != 0 || !view.Builtin || len(view.Formats) == 0 {
		t.Fatalf("%+v", view)
	}
	if resp, body := h.do(http.MethodGet, sub, nil, agent("Happ/3.4.1")); resp.StatusCode != http.StatusOK || len(body) == 0 {
		t.Fatalf("a fetch with no rules: %d", resp.StatusCode)
	}

	// A save is refused whole, and names the place.
	bad := map[string]any{"builtin": true, "rules": []apps.Rule{{Name: "x", Enabled: true,
		When: apps.Group{Op: apps.And, Conds: []apps.Cond{{Header: "User-Agent", Op: apps.OpRegex, Value: "("}}, Groups: []apps.Inner{}}, Do: apps.Action{Respond: apps.Respond403}}}}
	resp, out = h.do(http.MethodPut, api+"/sub-rules", bad, csrf)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(out), "resp_regex") || !strings.Contains(string(out), "body.rules[0].when.conds[0].value") {
		t.Fatalf("a regular expression that does not compile: %d %s", resp.StatusCode, out)
	}
	// A change is the session's own: without its CSRF token nothing is saved.
	if resp, _ := h.do(http.MethodPut, api+"/sub-rules", bad, nil); resp.StatusCode == http.StatusOK {
		t.Fatal("a save without the session's CSRF token went through")
	}

	// Rules: refuse curl, give Happ a Clash profile, close the connection on a bot.
	cond := func(ua string) apps.Group {
		return apps.Group{Op: apps.And, Conds: []apps.Cond{{Header: "User-Agent", Op: apps.OpContains, Value: ua}}, Groups: []apps.Inner{}}
	}
	rules := []apps.Rule{
		{Name: "no curl", Enabled: true, When: cond("curl"), Do: apps.Action{Respond: apps.Respond403, Headers: []apps.Header{{Name: "X-Why", Value: "scraper"}}}},
		{Name: "happ clash", Enabled: true, When: cond("happ"), Do: apps.Action{Respond: apps.RespondServe, Format: "clash"}},
		{Name: "bots", Enabled: true, When: cond("badbot"), Do: apps.Action{Respond: apps.RespondDrop}},
		{Name: "browsers", Enabled: true, When: cond("mozilla"), Do: apps.Action{Respond: apps.RespondHTML, HTML: "<h1>Use Happ</h1>"}},
	}
	resp, out = h.do(http.MethodPut, api+"/sub-rules", map[string]any{"builtin": true, "rules": rules}, csrf)
	jsonOf(t, resp, out, http.StatusOK, &view)
	if len(view.Rules) != 4 || view.Rules[1].Name != "happ clash" || view.Rules[1].Do.Format != "clash" {
		t.Fatalf("saved rules: %+v", view.Rules)
	}
	// The next fetch sees them: the subscriptions' config is not kept past a change.
	resp, body := h.do(http.MethodGet, sub, nil, agent("curl/8.9"))
	if resp.StatusCode != http.StatusForbidden || resp.Header.Get("X-Why") != "scraper" {
		t.Fatalf("curl: %d %v", resp.StatusCode, resp.Header)
	}
	resp, body = h.do(http.MethodGet, sub, nil, agent("Happ/3.4.1"))
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/yaml") || !bytes.Contains(body, []byte("proxies:")) {
		t.Fatalf("Happ forced to Clash: %d %v", resp.StatusCode, resp.Header)
	}
	resp, body = h.do(http.MethodGet, sub, nil, browser)
	if resp.StatusCode != http.StatusOK || string(body) != "<h1>Use Happ</h1>" || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatalf("the admin's page: %d %s %v", resp.StatusCode, body, resp.Header)
	}
	// The connection is closed, with no answer.
	req, _ := http.NewRequest(http.MethodGet, h.ts.URL+sub, nil)
	req.Header.Set("User-Agent", "BadBot/1.0")
	// On a connection of its own: a client resends a GET that a connection it reused closed on it.
	tr := h.client.Transport.(*http.Transport).Clone()
	tr.DisableKeepAlives = true
	defer tr.CloseIdleConnections()
	if r, err := (&http.Client{Transport: tr}).Do(req); err == nil {
		r.Body.Close()
		t.Fatalf("an answer came to a dropped connection: %d", r.StatusCode)
	}

	// The tester shows what would happen, for rules not yet saved too.
	probe := func(body map[string]any) (test ruleTest) {
		t.Helper()
		resp, out := h.do(http.MethodPost, api+"/sub-rules/test", body, csrf)
		jsonOf(t, resp, out, http.StatusOK, &test)
		return test
	}
	test := probe(map[string]any{"user_agent": "Happ/3.4.1", "headers": []apps.Header{}})
	if test.Rule != "happ clash" || test.Position != 2 || test.Respond != "serve" || test.Format != "clash" || test.AppID != "happ" {
		t.Errorf("saved rules: %+v", test)
	}
	test = probe(map[string]any{"user_agent": "ClashforWindows/0.20.39", "headers": []apps.Header{}})
	if test.Rule != apps.RuleOld || test.Respond != "stub" || test.Position != 0 || !strings.Contains(test.Text, "not supported") && !strings.Contains(test.Text, "не поддерживается") {
		t.Errorf("a discontinued app: %+v", test)
	}
	test = probe(map[string]any{"user_agent": "curl/8.9", "headers": []apps.Header{}, "rules": []apps.Rule{}, "builtin": false})
	if test.Rule != "" || test.Respond != "serve" || test.Status != 200 {
		t.Errorf("a draft without rules: %+v", test)
	}
	resp, out = h.do(http.MethodPost, api+"/sub-rules/test", map[string]any{"user_agent": "x", "headers": []apps.Header{{Name: "Bad Name", Value: "1"}}}, csrf)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(out), "body.headers[0].name") {
		t.Errorf("a header name that is not one: %d %s", resp.StatusCode, out)
	}

	// Blocked apps: the app gets the admin's words, the page's /info lists it.
	resp, out = h.do(http.MethodPut, api+"/sub-apps", map[string]any{"blocked": []string{"nonesuch"}, "text": ""}, csrf)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(out), "app_unknown") {
		t.Fatalf("an unknown app: %d %s", resp.StatusCode, out)
	}
	var appsView struct {
		Apps []struct {
			ID      string `json:"id"`
			Blocked bool   `json:"blocked"`
		} `json:"apps"`
		Preview string `json:"preview"`
	}
	resp, out = h.do(http.MethodPut, api+"/sub-apps", map[string]any{"blocked": []string{"incy"}, "text": "  Use Happ  "}, csrf)
	jsonOf(t, resp, out, http.StatusOK, &appsView)
	if appsView.Preview != "Use Happ" || len(appsView.Apps) != len(apps.Catalog) {
		t.Fatalf("%+v", appsView)
	}
	for _, a := range appsView.Apps {
		if a.Blocked != (a.ID == "incy") {
			t.Errorf("%s blocked: %v", a.ID, a.Blocked)
		}
	}
	resp, body = h.do(http.MethodGet, sub, nil, agent("INCY/2.1.0"))
	if resp.StatusCode != http.StatusOK || stubText(t, body) != "Use Happ" {
		t.Fatalf("a blocked app: %d %.100s", resp.StatusCode, body)
	}
	var info struct {
		Blocked []string `json:"blocked_apps"`
	}
	resp, out = h.do(http.MethodGet, sub+"/info", nil, nil)
	jsonOf(t, resp, out, http.StatusOK, &info)
	if len(info.Blocked) != 1 || info.Blocked[0] != "incy" {
		t.Errorf("/info: %v", info.Blocked)
	}
	// The tester shows the format the answer goes out in: the links of a stub in place of
	// INCY's xray configs, the page for a messenger's preview.
	if test = probe(map[string]any{"user_agent": "INCY/2.1.0", "headers": []apps.Header{}}); test.Respond != "stub" || test.Format != "uri" {
		t.Errorf("a blocked app of xray configs: %+v", test)
	}
	if test = probe(map[string]any{"user_agent": "TelegramBot (like TwitterBot)", "headers": []apps.Header{}}); test.Respond != "serve" || test.Format != "html" {
		t.Errorf("a messenger's preview: %+v", test)
	}
	// The page goes out before a rule's stub, to a messenger's preview and to a browser alike.
	stubAll := []apps.Rule{{Name: "stub all", Enabled: true, When: cond("/"), Do: apps.Action{Respond: apps.RespondStub}}}
	for _, hd := range []map[string]any{
		{"user_agent": "TelegramBot/1.0 (like TwitterBot)", "headers": []apps.Header{}, "rules": stubAll},
		{"user_agent": "Mozilla/5.0", "headers": []apps.Header{{Name: "Accept", Value: "text/html"}}, "rules": stubAll},
	} {
		if test = probe(hd); test.Rule != "stub all" || test.Status != 200 || test.Format != "html" || test.Text != "" {
			t.Errorf("%s under a stub rule: %+v", hd["user_agent"], test)
		}
	}
	if test = probe(map[string]any{"user_agent": "v2rayNG/1.10.20", "headers": []apps.Header{}, "rules": stubAll}); test.Format != "uri" || test.Text == "" {
		t.Errorf("an app under a stub rule: %+v", test)
	}

	// The log: newest first, what asked and what it came to, and no token.
	var log struct {
		Days  int `json:"days"`
		Items []struct {
			IP      string `json:"ip"`
			Agent   string `json:"agent"`
			App     string `json:"app"`
			AppName string `json:"app_name"`
			Rule    string `json:"rule"`
			Format  string `json:"format"`
			Outcome string `json:"outcome"`
		} `json:"items"`
	}
	userLog := func() {
		t.Helper()
		resp, out := h.do(http.MethodGet, api+"/users/"+itoa(u.ID)+"/sub-log", nil, nil)
		jsonOf(t, resp, out, http.StatusOK, &log)
		if strings.Contains(string(out), u.SubToken) {
			t.Fatalf("the token is in the log: %s", out)
		}
	}
	userLog()
	if log.Days != 14 || len(log.Items) != 6 {
		t.Fatalf("%d days, %d entries: %+v", log.Days, len(log.Items), log.Items)
	}
	first, oldest := log.Items[0], log.Items[len(log.Items)-1]
	if first.App != "incy" || first.AppName != "INCY" || first.Rule != "apps" || first.Format != "uri" || first.Outcome != "stub" || first.IP == "" {
		t.Errorf("the newest: %+v", first)
	}
	if oldest.App != "happ" || oldest.Rule != "" || oldest.Format != "xray" || oldest.Outcome != "served" {
		t.Errorf("the oldest: %+v", oldest)
	}
	byRule := map[string]string{}
	for _, it := range log.Items {
		byRule[it.Rule+"/"+it.Outcome] = it.Format
	}
	for key, format := range map[string]string{"no curl/blocked": "", "happ clash/served": "clash", "bots/dropped": "", "browsers/blocked": ""} {
		if got, ok := byRule[key]; !ok || got != format {
			t.Errorf("%s: format %q (found: %v)", key, got, ok)
		}
	}

	// The log is off at 0 days: nothing more is written, and the page says so.
	resp, out = h.do(http.MethodPatch, api+"/settings", map[string]any{"sub_log_days": 0}, csrf)
	jsonOf(t, resp, out, http.StatusOK, nil)
	h.do(http.MethodGet, sub, nil, agent("Happ/3.4.1"))
	userLog()
	if log.Days != 0 || len(log.Items) != 6 {
		t.Fatalf("with the log off: %d days, %d entries", log.Days, len(log.Items))
	}

	// Maintenance forgets what is older than the retention.
	resp, out = h.do(http.MethodPatch, api+"/settings", map[string]any{"sub_log_days": 14}, csrf)
	jsonOf(t, resp, out, http.StatusOK, nil)
	if _, err := h.st.Q.AddSubLog(ctx, db.AddSubLogParams{UserID: u.ID, At: h.now.Add(-15 * 24 * time.Hour).Unix(), Ip: "192.0.2.1", Agent: "old", Outcome: "served", MaxPerMinute: 30}); err != nil {
		t.Fatal(err)
	}
	userLog()
	if len(log.Items) != 7 {
		t.Fatalf("an old entry is listed until it is pruned: %d", len(log.Items))
	}
	h.p.maintain(ctx)
	userLog()
	if len(log.Items) != 6 {
		t.Fatalf("maintenance left %d entries, want the 6 of the last days", len(log.Items))
	}
	if resp, _ := h.do(http.MethodPatch, api+"/settings", map[string]any{"sub_log_days": 91}, csrf); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("91 days: %d", resp.StatusCode)
	}
}
