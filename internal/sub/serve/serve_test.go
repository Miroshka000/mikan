package serve

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"mikan/internal/proto"
	"mikan/internal/sub/apps"
	"mikan/internal/sub/model"
	"mikan/internal/sub/routing"
	"mikan/internal/sub/subtest"
	"mikan/internal/sub/xray"
)

const testToken = "0123456789abcdefghijklmn"

// fakeSub is a subscription with the servers of the golden files' default user, that
// remembers what it was asked and what the log was told. The errors, when set, are what
// its methods fail with.
type fakeSub struct {
	cfg         Config
	user        User
	userErr     error
	keysErr     error
	keys        []Device
	inboundsErr error
	bound       int64
	boundErr    error
	boundCalls  int
	groups      AppGroups
	groupsErr   error
	groupsOf    [][]model.Proxy // the proxies each call of Groups was given
	fetched     int
	log         []LogEntry
}

func (f *fakeSub) Config() Config                     { return f.cfg }
func (f *fakeSub) User(context.Context) (User, error) { return f.user, f.userErr }
func (f *fakeSub) Bound(context.Context) (int64, error) {
	f.boundCalls++
	return f.bound, f.boundErr
}
func (f *fakeSub) Bot(context.Context) string { return "" }
func (f *fakeSub) Keys(_ context.Context, dev Device) (proto.Slot, error) {
	f.keys = append(f.keys, dev)
	return subtest.Keys, f.keysErr
}
func (f *fakeSub) Inbounds(context.Context) ([]model.Inbound, error) {
	if f.inboundsErr != nil {
		return nil, f.inboundsErr
	}
	return subtest.Defaults("").Build.Inbounds, nil
}
func (f *fakeSub) Fetched(context.Context, string) { f.fetched++ }
func (f *fakeSub) Groups(_ context.Context, ps []model.Proxy) (AppGroups, error) {
	f.groupsOf = append(f.groupsOf, ps)
	return f.groups, f.groupsErr
}
func (f *fakeSub) Record(_ context.Context, e LogEntry) { f.log = append(f.log, e) }

type fakeSource struct{ sub *fakeSub }

func (s fakeSource) Subscription(_ context.Context, token string) (Subscription, error) {
	if token != testToken {
		return nil, ErrNotFound
	}
	return s.sub, nil
}

// newSub is a subscription served by policy.
func newSub(t *testing.T, p apps.Policy) *fakeSub {
	t.Helper()
	r, err := apps.NewResponder(p)
	if err != nil {
		t.Fatal(err)
	}
	return &fakeSub{
		cfg:  Config{Brand: "Mikan", Nodes: subtest.Defaults("").Build.Nodes, Responses: r, Lang: "en"},
		user: User{Token: testToken, Name: "u", State: StateActive},
	}
}

// testNow is the time of the handlers of handlerOf.
var testNow = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

func handlerOf(sub *fakeSub) *Handler {
	return New(fakeSource{sub}, func() time.Time { return testNow }, func(*http.Request) string { return "192.0.2.7" })
}

// fetch is a GET of the subscription by an app with the User-Agent ua.
func fetch(t *testing.T, sub *fakeSub, ua string, kv ...string) *httptest.ResponseRecorder {
	t.Helper()
	return fetchAs(t, sub, http.MethodGet, "/"+testToken, ua, kv...)
}

func fetchAs(t *testing.T, sub *fakeSub, method, target, ua string, kv ...string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, nil)
	if ua != "" {
		r.Header.Set("User-Agent", ua)
	}
	for i := 0; i+1 < len(kv); i += 2 {
		r.Header.Set(kv[i], kv[i+1])
	}
	w := httptest.NewRecorder()
	handlerOf(sub).Serve(w, r, testToken)
	return w
}

func rule(name, ua string, do apps.Action) apps.Rule {
	return apps.Rule{Name: name, Enabled: true, Do: do,
		When: apps.Group{Op: apps.And, Conds: []apps.Cond{{Header: "User-Agent", Op: apps.OpContains, Value: ua}}}}
}

// stubName is the name of the placeholder entry of a stub in the format of a link list.
func stubName(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(w.Body.String())
	if err != nil {
		t.Fatalf("not links: %q", w.Body.String())
	}
	_, frag, _ := strings.Cut(string(raw), "#")
	name, err := url.PathUnescape(strings.TrimSpace(frag))
	if err != nil {
		t.Fatal(err)
	}
	return name
}

func oneEntry(t *testing.T, sub *fakeSub) LogEntry {
	t.Helper()
	if len(sub.log) != 1 {
		t.Fatalf("the log has %d entries, want 1: %+v", len(sub.log), sub.log)
	}
	return sub.log[0]
}

func TestServedRequestIsLogged(t *testing.T) {
	sub := newSub(t, apps.Policy{NoBuiltin: true})
	w := fetch(t, sub, "Happ/3.4.1")
	if w.Code != 200 || len(sub.keys) != 1 {
		t.Fatalf("%d, %d keys given", w.Code, len(sub.keys))
	}
	e := oneEntry(t, sub)
	want := LogEntry{At: testNow, IP: "192.0.2.7", Agent: "Happ/3.4.1", App: "happ", Format: FormatURI, Outcome: OutcomeServed}
	if e != want {
		t.Errorf("%+v, want %+v", e, want)
	}
	// The log has no token: nothing of the link, of the keys or of the profile.
	if got := strings.Join([]string{e.IP, e.Agent, e.App, e.Rule, e.Format, string(e.Outcome)}, " "); strings.Contains(got, testToken) || strings.Contains(got, subtest.Keys.UUID) {
		t.Errorf("secrets in the log: %q", got)
	}
}

func TestLogBoundsTheAgent(t *testing.T) {
	sub := newSub(t, apps.Policy{NoBuiltin: true})
	fetch(t, sub, "Happ/"+strings.Repeat("9", 5000))
	if n := len([]rune(oneEntry(t, sub).Agent)); n != MaxAgent {
		t.Errorf("%d letters of the User-Agent kept", n)
	}
}

func TestUnknownTokenIsNotLogged(t *testing.T) {
	sub := newSub(t, apps.Policy{})
	w := httptest.NewRecorder()
	handlerOf(sub).Serve(w, httptest.NewRequest(http.MethodGet, "/nothing", nil), "nothing")
	if w.Code != 404 || len(sub.log) != 0 {
		t.Errorf("%d, %d entries", w.Code, len(sub.log))
	}
}

func TestHeadIsNotLogged(t *testing.T) {
	sub := newSub(t, apps.Policy{NoBuiltin: true})
	w := fetchAs(t, sub, http.MethodHead, "/"+testToken, "Happ/3.4.1")
	if w.Code != 200 || w.Header().Get("Subscription-Userinfo") == "" || len(sub.keys) != 0 || len(sub.log) != 0 {
		t.Errorf("%d, %d keys, %d entries: a HEAD peeks at the headers", w.Code, len(sub.keys), len(sub.log))
	}
}

func TestRuleAnswers403And404(t *testing.T) {
	sub := newSub(t, apps.Policy{NoBuiltin: true, Rules: []apps.Rule{
		rule("no curl", "curl", apps.Action{Respond: apps.Respond403, Headers: []apps.Header{{Name: "X-Why", Value: "scraper"}}}),
		rule("no wget", "wget", apps.Action{Respond: apps.Respond404}),
	}})
	w := fetch(t, sub, "curl/8.9")
	if w.Code != 403 || w.Header().Get("X-Why") != "scraper" || w.Header().Get("Cache-Control") != "no-store" || len(sub.keys) != 0 {
		t.Errorf("403: %d %v, %d keys", w.Code, w.Header(), len(sub.keys))
	}
	if e := oneEntry(t, sub); e.Outcome != OutcomeBlocked || e.Rule != "no curl" || e.Format != "" {
		t.Errorf("%+v", e)
	}
	w = fetch(t, sub, "Wget/1.21")
	if w.Code != 404 || w.Body.String() != "404 Not Found\n" || len(sub.keys) != 0 {
		t.Errorf("404: %d %q", w.Code, w.Body)
	}
	if e := sub.log[1]; e.Outcome != OutcomeBlocked || e.Rule != "no wget" {
		t.Errorf("%+v", e)
	}
	// A HEAD is refused the same way, and not logged.
	if w := fetchAs(t, sub, http.MethodHead, "/"+testToken, "curl/8.9"); w.Code != 403 || len(sub.log) != 2 {
		t.Errorf("HEAD: %d, %d entries", w.Code, len(sub.log))
	}
}

func TestRulePage(t *testing.T) {
	sub := newSub(t, apps.Policy{NoBuiltin: true, Rules: []apps.Rule{rule("page", "mozilla", apps.Action{
		Respond: apps.RespondHTML, HTML: "<h1>Use Happ</h1>",
		// A rule cannot loosen what the page is served with.
		Headers: []apps.Header{{Name: "Content-Security-Policy", Value: "default-src *"}, {Name: "X-Own", Value: "1"}},
	})}})
	w := fetch(t, sub, "Mozilla/5.0", "Accept", "text/html")
	if w.Code != 200 || w.Body.String() != "<h1>Use Happ</h1>" || w.Header().Get("X-Own") != "1" {
		t.Fatalf("%d %q %v", w.Code, w.Body, w.Header())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content type %q", ct)
	}
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'none'") || strings.Contains(csp, "*") {
		t.Errorf("the page runs under %q", csp)
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("nosniff")
	}
	if e := oneEntry(t, sub); e.Outcome != OutcomeBlocked || e.Rule != "page" {
		t.Errorf("%+v", e)
	}
	if w := fetchAs(t, sub, http.MethodHead, "/"+testToken, "Mozilla/5.0"); w.Code != 200 || w.Body.Len() != 0 {
		t.Errorf("HEAD: %d %q", w.Code, w.Body)
	}
}

// A real server lets the handler take the connection: the client gets nothing.
func TestRuleDropsTheConnection(t *testing.T) {
	sub := newSub(t, apps.Policy{NoBuiltin: true, Rules: []apps.Rule{rule("drop", "badbot", apps.Action{Respond: apps.RespondDrop})}})
	// The client sees the connection closed before the handler has returned: the log is read
	// once it has.
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		handlerOf(sub).Serve(w, r, testToken)
	}))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/"+testToken, nil)
	req.Header.Set("User-Agent", "BadBot/1")
	resp, err := srv.Client().Do(req)
	if err == nil {
		resp.Body.Close()
		t.Fatalf("an answer came: %d", resp.StatusCode)
	}
	<-done
	if e := oneEntry(t, sub); e.Outcome != OutcomeDropped || e.Rule != "drop" || e.App != "" {
		t.Errorf("%+v", e)
	}
}

// Where the connection cannot be taken over (HTTP/2) the handler aborts, after the log.
func TestRuleDropAbortsWhereTheConnectionIsNotTheHandlers(t *testing.T) {
	sub := newSub(t, apps.Policy{NoBuiltin: true, Rules: []apps.Rule{rule("drop", "badbot", apps.Action{Respond: apps.RespondDrop})}})
	defer func() {
		if p := recover(); !errors.Is(asError(p), http.ErrAbortHandler) {
			t.Errorf("panic %v, want http.ErrAbortHandler", p)
		}
		if e := oneEntry(t, sub); e.Outcome != OutcomeDropped {
			t.Errorf("%+v", e)
		}
	}()
	fetch(t, sub, "BadBot/1")
	t.Fatal("the handler returned")
}

func asError(p any) error {
	err, _ := p.(error)
	return err
}

func TestRuleForcesTheFormat(t *testing.T) {
	sub := newSub(t, apps.Policy{NoBuiltin: true, Rules: []apps.Rule{rule("happ gets clash", "happ", apps.Action{Format: FormatClash,
		Headers: []apps.Header{{Name: "Profile-Update-Interval", Value: "6"}}})}})
	w := fetch(t, sub, "Happ/3.4.1", "X-Hwid", "abc")
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/yaml") || !strings.Contains(w.Body.String(), "proxies:") {
		t.Fatalf("%d %v\n%.200s", w.Code, w.Header(), w.Body)
	}
	// The rule's headers go over the panel's own.
	if got := w.Header().Get("Profile-Update-Interval"); got != "6" {
		t.Errorf("Profile-Update-Interval %q", got)
	}
	if e := oneEntry(t, sub); e.Format != FormatClash || e.Rule != "happ gets clash" || e.App != "happ" || e.Outcome != OutcomeServed {
		t.Errorf("%+v", e)
	}
	// ?format= does not undo it: the rule is the admin's word about this client.
	if w := fetchAs(t, sub, http.MethodGet, "/"+testToken+"?format=uri", "Happ/3.4.1"); !strings.HasPrefix(w.Header().Get("Content-Type"), "text/yaml") {
		t.Errorf("?format= wins: %v", w.Header())
	}
}

// Every format a rule may name is one the handler can write.
func TestEveryNamedFormatIsServed(t *testing.T) {
	for _, f := range Formats {
		sub := newSub(t, apps.Policy{NoBuiltin: true, Rules: []apps.Rule{rule("f", "curl", apps.Action{Format: f})}})
		if w := fetch(t, sub, "curl/8.9"); w.Code != 200 || w.Body.Len() == 0 || sub.log[0].Format != f {
			t.Errorf("%s: %d, %d bytes, %+v", f, w.Code, w.Body.Len(), sub.log)
		}
	}
	for _, f := range Formats {
		if err := rule("x", "curl", apps.Action{Format: f}).Check("r", apps.CheckOpts{Formats: Formats}); err != nil {
			t.Errorf("%s is named by Formats and refused by Check: %v", f, err)
		}
	}
}

func TestRuleSkipsHWID(t *testing.T) {
	sub := newSub(t, apps.Policy{NoBuiltin: true, Rules: []apps.Rule{rule("tv", "smarttv", apps.Action{SkipHWID: true})}})
	fetch(t, sub, "SmartTV/1")
	fetch(t, sub, "Happ/3.4.1")
	if len(sub.keys) != 2 || !sub.keys[0].SkipHWID || sub.keys[1].SkipHWID {
		t.Errorf("devices asked: %+v", sub.keys)
	}
}

func TestBlockedAppGetsAStubWithoutKeys(t *testing.T) {
	sub := newSub(t, apps.Policy{NoBuiltin: true, Blocked: []string{"shadowrocket", "happ"}})
	w := fetch(t, sub, "Shadowrocket/2592 CFNetwork/1568")
	if w.Code != 200 || len(sub.keys) != 0 {
		t.Fatalf("%d, %d keys: a blocked app must not take a device's place", w.Code, len(sub.keys))
	}
	if got := stubName(t, w); got != "⛔ This app is not supported — install INCY, Koala Clash or Mikan" {
		t.Errorf("stub: %q", got)
	}
	if e := oneEntry(t, sub); e.Outcome != OutcomeStub || e.Rule != apps.RuleApps || e.App != "shadowrocket" || e.Format != FormatURI {
		t.Errorf("%+v", e)
	}
	// The admin's own words, in the format of the app: a Clash app gets a Clash stub.
	sub.cfg.Responses, _ = apps.NewResponder(apps.Policy{NoBuiltin: true, Blocked: []string{"koala"}, Text: "Use Happ\nfrom the store"})
	w = fetch(t, sub, "koala-clash/1.4.1")
	if !strings.HasPrefix(w.Header().Get("Content-Type"), "text/yaml") || !strings.Contains(w.Body.String(), "Use Happ from the store") {
		t.Errorf("%v\n%.300s", w.Header(), w.Body)
	}
	// An app that is not blocked is served.
	fetch(t, sub, "INCY/2.1.0")
	if e := sub.log[len(sub.log)-1]; e.Outcome != OutcomeServed || e.App != "incy" || len(sub.keys) != 1 {
		t.Errorf("an allowed app: %+v, %d keys", e, len(sub.keys))
	}
}

func TestBuiltinRuleAnswersADiscontinuedApp(t *testing.T) {
	sub := newSub(t, apps.Policy{})
	w := fetch(t, sub, "ClashforWindows/0.20.39")
	if len(sub.keys) != 0 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/yaml") || !strings.Contains(w.Body.String(), "This app is not supported") {
		t.Errorf("%d %v\n%.300s", w.Code, w.Header(), w.Body)
	}
	if e := oneEntry(t, sub); e.Rule != apps.RuleOld || e.Outcome != OutcomeStub {
		t.Errorf("%+v", e)
	}
}

func TestStubsOfOtherReasonsAreLogged(t *testing.T) {
	sub := newSub(t, apps.Policy{NoBuiltin: true})
	sub.keysErr = ErrDeviceLimit
	fetch(t, sub, "Happ/3.4.1", "X-Hwid", "abc")
	if e := oneEntry(t, sub); e.Outcome != OutcomeStub || e.Rule != "" || e.App != "happ" {
		t.Errorf("%+v", e)
	}
	sub = newSub(t, apps.Policy{NoBuiltin: true})
	sub.user.State = StateExpired
	sub.cfg.InactiveServer = true
	fetch(t, sub, "Happ/3.4.1")
	if e := oneEntry(t, sub); e.Outcome != OutcomeStub {
		t.Errorf("an ended subscription: %+v", e)
	}
	sub = newSub(t, apps.Policy{NoBuiltin: true})
	sub.cfg.Nodes = nil
	fetch(t, sub, "Happ/3.4.1")
	if e := oneEntry(t, sub); e.Outcome != OutcomeStub {
		t.Errorf("nothing to connect to: %+v", e)
	}
}

func TestPageIsLogged(t *testing.T) {
	sub := newSub(t, apps.Policy{NoBuiltin: true})
	h := handlerOf(sub)
	h.SetPage(func(w http.ResponseWriter, _ *http.Request, _ Config) { _, _ = w.Write([]byte("page")) })
	r := httptest.NewRequest(http.MethodGet, "/"+testToken, nil)
	r.Header.Set("User-Agent", "Mozilla/5.0")
	r.Header.Set("Accept", "text/html")
	w := httptest.NewRecorder()
	h.Serve(w, r, testToken)
	if w.Body.String() != "page" {
		t.Fatalf("%q", w.Body)
	}
	if e := oneEntry(t, sub); e.Outcome != OutcomeServed || e.Format != FormatHTML || e.App != "" {
		t.Errorf("%+v", e)
	}
}

func TestResolve(t *testing.T) {
	h := http.Header{"User-Agent": {"Happ/3.4.1"}}
	if got := Resolve(h, "", apps.Decision{}, false); got != FormatURI {
		t.Errorf("%s", got)
	}
	if got := Resolve(h, "clash", apps.Decision{}, true); got != FormatClash {
		t.Errorf("?format=: %s", got)
	}
	// Happ runs whole xray configs while the switch is on, unless the request asks for links.
	if got := Resolve(h, "", apps.Decision{}, true); got != FormatXray {
		t.Errorf("Happ: %s", got)
	}
	if got := Resolve(h, "uri", apps.Decision{}, true); got != FormatURI {
		t.Errorf("Happ asking for links: %s", got)
	}
	if got := Resolve(http.Header{"User-Agent": {"v2rayNG/1.10.20"}}, "", apps.Decision{}, true); got != FormatURI {
		t.Errorf("an xray app off the list: %s", got)
	}
	for _, q := range []string{"xray", "json", "v2ray-json"} {
		if got := Resolve(http.Header{"User-Agent": {"curl/8.9"}}, q, apps.Decision{}, false); got != FormatXray {
			t.Errorf("?format=%s: %s", q, got)
		}
	}
	if got := Resolve(h, "clash", apps.Decision{Action: apps.Action{Format: FormatSingBox}}, true); got != FormatSingBox {
		t.Errorf("a rule's format: %s", got)
	}
}

// Happ gets the xray JSON profile, a list of whole configs, while the switch is on; with it
// off, or for a stub, the links.
func TestXrayJSONForTheAppsOfTheList(t *testing.T) {
	sub := newSub(t, apps.Policy{NoBuiltin: true})
	sub.cfg.XrayJSON = true
	w := fetch(t, sub, "Happ/3.4.1", "X-Hwid", "abc")
	var configs []map[string]any
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") || json.Unmarshal(w.Body.Bytes(), &configs) != nil ||
		len(configs) < 2 || configs[0]["remarks"] != "Auto" {
		t.Fatalf("%d %v\n%.300s", w.Code, w.Header(), w.Body)
	}
	if e := oneEntry(t, sub); e.Format != FormatXray || e.Outcome != OutcomeServed {
		t.Errorf("%+v", e)
	}
	sub.keysErr = ErrDeviceLimit
	if w := fetch(t, sub, "Happ/3.4.1", "X-Hwid", "abc"); !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") || !strings.Contains(stubName(t, w), "device places") {
		t.Errorf("the stub is a link: %v\n%.200s", w.Header(), w.Body)
	}
	sub.keysErr, sub.cfg.XrayJSON = nil, false
	if w := fetch(t, sub, "Happ/3.4.1", "X-Hwid", "abc"); !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
		t.Errorf("switched off: %v", w.Header())
	}
}

// hwidHeaders are the device binding's headers of an answer.
func hwidHeaders(w *httptest.ResponseRecorder) map[string]string {
	out := map[string]string{}
	for k := range w.Header() {
		if strings.HasPrefix(k, "X-Hwid-") {
			out[k] = w.Header().Get(k)
		}
	}
	return out
}

// Every reason Keys gives for no keys has a stub of its own, in the panel's language, and
// the headers the apps that read the device binding show.
func TestKeysErrorStubs(t *testing.T) {
	noHWID := map[string]string{"X-Hwid-Not-Supported": "true"}
	for _, c := range []struct {
		name    string
		err     error
		blocked []string
		en, ru  string
		hdr     map[string]string
	}{
		{"no hwid", ErrNoHWID, nil,
			"⛔ The app does not send a device ID — install Happ, Koala Clash or INCY",
			"⛔ Приложение не сообщает ID устройства — поставьте Happ, Koala Clash или INCY", noHWID},
		{"no hwid, some apps blocked", ErrNoHWID, []string{"koala"},
			"⛔ The app does not send a device ID — install Happ or INCY",
			"⛔ Приложение не сообщает ID устройства — поставьте Happ или INCY", noHWID},
		{"no hwid, one app left", ErrNoHWID, []string{"happ", "koala"},
			"⛔ The app does not send a device ID — install INCY",
			"⛔ Приложение не сообщает ID устройства — поставьте INCY", noHWID},
		{"no hwid, every app blocked", ErrNoHWID, []string{"happ", "koala", "incy"},
			"⛔ The app does not send a device ID — open the subscription link in a browser",
			"⛔ Приложение не сообщает ID устройства — откройте ссылку подписки в браузере", noHWID},
		{"banned", ErrDeviceBanned, nil,
			"⛔ This device is blocked",
			"⛔ Устройство заблокировано", map[string]string{}},
		{"unbound until", &UnboundError{Until: testNow.Add(90 * time.Minute)}, nil,
			"⏸ This device was unbound — it can connect again in 2 h",
			"⏸ Устройство отвязано — подключится снова через 2 ч", map[string]string{}},
		{"unbound, the time is past", &UnboundError{Until: testNow.Add(-time.Minute)}, nil,
			"⏸ This device was unbound — it can connect again in 1 h",
			"⏸ Устройство отвязано — подключится снова через 1 ч", map[string]string{}},
		{"unbound forever", &UnboundError{Until: testNow.Add(time.Hour), Forever: true}, nil,
			"⏸ This device was unbound — only the administrator can let it connect again",
			"⏸ Устройство отвязано — подключить его снова может только администратор", map[string]string{}},
	} {
		for lang, want := range map[string]string{"en": c.en, "ru": c.ru} {
			sub := newSub(t, apps.Policy{NoBuiltin: true, Blocked: c.blocked})
			sub.cfg.Lang = lang
			sub.keysErr = c.err
			w := fetch(t, sub, "v2rayNG/1.10.20")
			if w.Code != 200 || len(sub.keys) != 1 {
				t.Fatalf("%s/%s: %d, %d keys asked", c.name, lang, w.Code, len(sub.keys))
			}
			if got := stubName(t, w); got != want {
				t.Errorf("%s/%s: %q, want %q", c.name, lang, got, want)
			}
			if got := hwidHeaders(w); !maps.Equal(got, c.hdr) {
				t.Errorf("%s/%s: headers %v, want %v", c.name, lang, got, c.hdr)
			}
			if e := oneEntry(t, sub); e.Outcome != OutcomeStub || e.Format != FormatURI {
				t.Errorf("%s/%s: %+v", c.name, lang, e)
			}
			// A device that gets no keys took no profile.
			if sub.fetched != 0 {
				t.Errorf("%s/%s: Fetched %d times", c.name, lang, sub.fetched)
			}
		}
	}
}

// A subscription that cannot connect gets the notice of its state, or the admin's words.
func TestInactiveNotice(t *testing.T) {
	for _, c := range []struct {
		state      State
		lang, text string
		want       string
	}{
		{StateLimited, "en", "", "⛔ The traffic is used up — buy more or renew in the bot"},
		{StateLimited, "ru", "", "⛔ Трафик закончился — докупите или продлите подписку в боте"},
		{StateDisabled, "en", "", "⛔ The subscription is turned off — write to support"},
		{StateDisabled, "ru", "", "⛔ Подписка отключена — напишите в поддержку"},
		{StateLimited, "en", "{name}, top up at {brand}\nnow", "u, top up at Mikan now"},
		{StateDisabled, "ru", "{name} — напишите в {brand}", "u — напишите в Mikan"},
		// The admin's text that fills to nothing gives way to the built-in one.
		{StateLimited, "en", "{tariff}", "⛔ The traffic is used up — buy more or renew in the bot"},
	} {
		sub := newSub(t, apps.Policy{NoBuiltin: true})
		sub.cfg.Lang, sub.cfg.InactiveServer, sub.cfg.InactiveText = c.lang, true, c.text
		sub.user.State = c.state
		w := fetch(t, sub, "v2rayNG/1.10.20")
		if got := stubName(t, w); w.Code != 200 || got != c.want {
			t.Errorf("%s/%s %q: %d %q, want %q", c.state, c.lang, c.text, w.Code, got, c.want)
		}
		if e := oneEntry(t, sub); e.Outcome != OutcomeStub || sub.fetched != 0 {
			t.Errorf("%s/%s: %+v, Fetched %d times", c.state, c.lang, e, sub.fetched)
		}
	}
}

func testGroups() AppGroups {
	return AppGroups{V: 1, Groups: []AppGroup{{ID: 7, Name: "Video", Mode: GroupOwn, Limit: ptr(10 << 30), Left: ptr(3 << 30), State: "active", Proxies: []string{"NL"}}}}
}

// The Mikan app that asks for the groups gets X-Mikan-Groups beside the profile, of the
// proxies that profile has; any other request goes without it, and without asking.
func TestGroupsHeader(t *testing.T) {
	for _, target := range []string{"/" + testToken, "/" + testToken + "?format=clash"} {
		sub := newSub(t, apps.Policy{NoBuiltin: true})
		sub.groups = testGroups()
		w := fetchAs(t, sub, http.MethodGet, target, "MikanApp/1.0", FeaturesHeader, "routes, groups")
		if want := testGroups().header(); w.Code != 200 || w.Header().Get(GroupsHeader) != want {
			t.Errorf("%s: %d %q, want %q", target, w.Code, w.Header().Get(GroupsHeader), want)
		}
		if len(sub.groupsOf) != 1 || len(sub.groupsOf[0]) == 0 {
			t.Errorf("%s: Groups asked of %v", target, sub.groupsOf)
		}
		w = fetchAs(t, sub, http.MethodGet, target, "MikanApp/1.0")
		if _, ok := w.Header()[GroupsHeader]; ok || len(sub.groupsOf) != 1 {
			t.Errorf("%s: groups sent unasked: %v, %d calls", target, w.Header(), len(sub.groupsOf))
		}
	}
}

// Groups that cannot be read cost the app its header, not its profile.
func TestGroupsErrorServesTheProfile(t *testing.T) {
	sub := newSub(t, apps.Policy{NoBuiltin: true})
	want := fetch(t, sub, "MikanApp/1.0").Body.String()
	sub.groups, sub.groupsErr = testGroups(), errors.New("db is gone")
	var logged bytes.Buffer
	h := handlerOf(sub)
	h.SetLogger(slog.New(slog.NewTextHandler(&logged, nil)))
	r := httptest.NewRequest(http.MethodGet, "/"+testToken, nil)
	r.Header.Set("User-Agent", "MikanApp/1.0")
	r.Header.Set(FeaturesHeader, "groups")
	w := httptest.NewRecorder()
	h.Serve(w, r, testToken)
	if _, ok := w.Header()[GroupsHeader]; w.Code != 200 || ok || w.Body.String() != want {
		t.Errorf("%d %v\n%.200s", w.Code, w.Header(), w.Body)
	}
	if e := sub.log[len(sub.log)-1]; e.Outcome != OutcomeServed {
		t.Errorf("%+v", e)
	}
	if !strings.Contains(logged.String(), `msg="subscription: the server groups for the Mikan app are not read" err="db is gone"`) {
		t.Errorf("logged %q", logged.String())
	}
}

// failingSource cannot read subscriptions at all.
type failingSource struct{}

func (failingSource) Subscription(context.Context, string) (Subscription, error) {
	return nil, errors.New("db is gone")
}

// A failure of the panel's own is a bare 500 that tells nothing of why, and is not logged:
// the request log is of what clients were given.
func TestPanelFailuresAre500(t *testing.T) {
	check := func(name string, sub *fakeSub, w *httptest.ResponseRecorder) {
		t.Helper()
		if w.Code != 500 || w.Body.String() != "internal error\n" || len(sub.log) != 0 {
			t.Errorf("%s: %d %q, %d entries", name, w.Code, w.Body, len(sub.log))
		}
	}
	// A source that fails has no subscription whose log could be written: only the answer
	// is checked.
	w := httptest.NewRecorder()
	New(failingSource{}, time.Now, func(*http.Request) string { return "" }).Serve(w, httptest.NewRequest(http.MethodGet, "/"+testToken, nil), testToken)
	if w.Code != 500 || w.Body.String() != "internal error\n" {
		t.Errorf("source: %d %q", w.Code, w.Body)
	}

	sub := newSub(t, apps.Policy{NoBuiltin: true})
	sub.userErr = errors.New("db is gone")
	check("user", sub, fetch(t, sub, "Happ/3.4.1"))
	if len(sub.keys) != 0 {
		t.Errorf("user: %d keys asked", len(sub.keys))
	}

	sub = newSub(t, apps.Policy{NoBuiltin: true})
	sub.keysErr = errors.New("db is gone")
	check("keys", sub, fetch(t, sub, "Happ/3.4.1"))

	sub = newSub(t, apps.Policy{NoBuiltin: true})
	sub.inboundsErr = errors.New("db is gone")
	check("inbounds", sub, fetch(t, sub, "Happ/3.4.1"))
	if len(sub.keys) != 1 || sub.fetched != 0 {
		t.Errorf("inbounds: %d keys asked, Fetched %d times", len(sub.keys), sub.fetched)
	}

	// A profile the renderers fail on for a reason of their own; nothing for the app to
	// connect to is a stub instead.
	sub = newSub(t, apps.Policy{NoBuiltin: true})
	w = httptest.NewRecorder()
	if out := handlerOf(sub).profileFailed(w, sub.cfg, FormatSingBox, errors.New("marshal")); out != "" || w.Code != 500 || w.Body.String() != "internal error\n" {
		t.Errorf("render: %q %d %q", out, w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	if out := handlerOf(sub).profileFailed(w, sub.cfg, FormatURI, model.ErrNoProxies); out != OutcomeStub ||
		stubName(t, w) != "⛔ There are no servers this app can use — open the subscription link in a browser" {
		t.Errorf("no proxies: %q %q", out, stubName(t, w))
	}
}

// Fetched is recorded once the inbounds are read, before the profile is made: a profile
// that turns out to have nothing in it counts all the same.
func TestFetchedBeforeTheProfile(t *testing.T) {
	sub := newSub(t, apps.Policy{NoBuiltin: true})
	fetch(t, sub, "Happ/3.4.1")
	if sub.fetched != 1 {
		t.Errorf("served: Fetched %d times", sub.fetched)
	}
	fetchAs(t, sub, http.MethodHead, "/"+testToken, "Happ/3.4.1")
	if sub.fetched != 1 {
		t.Errorf("HEAD: Fetched %d times", sub.fetched)
	}
	sub = newSub(t, apps.Policy{NoBuiltin: true})
	sub.cfg.Nodes = nil
	fetch(t, sub, "Happ/3.4.1")
	if e := oneEntry(t, sub); e.Outcome != OutcomeStub || sub.fetched != 1 {
		t.Errorf("nothing to connect to: %+v, Fetched %d times", e, sub.fetched)
	}
}

// {devices} counts the bound devices only with binding on and a text that uses it.
func TestAnnouncementDevices(t *testing.T) {
	u := User{Name: "u", Devices: ptr(3), Places: true}
	for _, c := range []struct {
		name     string
		announce string
		binding  bool
		boundErr error
		want     string
		calls    int
	}{
		{"counted", "{name}: {devices}", true, nil, "u: 2/3", 1},
		{"binding off", "{name}: {devices}", false, nil, "u: up to 3 at once", 0},
		{"not counted", "{name}: {devices}", true, errors.New("db is gone"), "u: up to 3 at once", 1},
		{"not used", "{name}", true, nil, "u", 0},
		{"none", "", true, nil, "", 0},
	} {
		sub := newSub(t, apps.Policy{NoBuiltin: true})
		sub.bound, sub.boundErr = 2, c.boundErr
		sub.cfg.Announce, sub.cfg.Binding = c.announce, c.binding
		if got := Announcement(context.Background(), sub, u, sub.cfg, testNow); got != c.want || sub.boundCalls != c.calls {
			t.Errorf("%s: %q, %d counts; want %q, %d", c.name, got, sub.boundCalls, c.want, c.calls)
		}
	}
}

// The title and the branding of a fetch show the bound devices, and the page is the one of
// the panel's own address.
func TestFetchShowsBoundDevices(t *testing.T) {
	sub := newSub(t, apps.Policy{NoBuiltin: true})
	sub.bound = 2
	sub.user.Devices, sub.user.Places = ptr(3), true
	sub.cfg.Binding, sub.cfg.Title = true, "{brand} · {devices}"
	sub.cfg.App = AppBrand{Enabled: true}
	sub.cfg.SubBase = "https://sub.example.com/s"
	w := fetch(t, sub, "Happ/3.4.1", "X-Hwid", "abc")
	for k, v := range map[string]string{
		"Profile-Title":           "base64:" + base64.StdEncoding.EncodeToString([]byte("Mikan · 2/3")),
		"Profile-Web-Page-Url":    "https://sub.example.com/s/" + testToken,
		"X-Brand-Renew-URL":       "https://sub.example.com/s/" + testToken,
		"X-Brand-Max-Devices":     "3",
		"X-Brand-Current-Devices": "2",
	} {
		if got := w.Header().Get(k); got != v {
			t.Errorf("%s: %q, want %q", k, got, v)
		}
	}
	// Once for the title, once for the branding.
	if sub.boundCalls != 2 {
		t.Errorf("counted %d times", sub.boundCalls)
	}
	// Devices that cannot be counted leave the count out, the limit in.
	sub.boundErr = errors.New("db is gone")
	w = fetch(t, sub, "Happ/3.4.1", "X-Hwid", "abc")
	if _, ok := w.Header()["X-Brand-Current-Devices"]; ok || w.Header().Get("X-Brand-Max-Devices") != "3" {
		t.Errorf("not counted: %v", w.Header())
	}
}

// ownClash is an admin's own Clash profile with a rule of its own to tell it by.
const ownClash = "proxy-groups:\n  - {name: Net, type: select, include-all-proxies: true}\nrules:\n  - DOMAIN-SUFFIX,mine.example,DIRECT\n  - MATCH,Net\n"

// Stash takes no own profile: with a Clash template of the admin's it gets the built-in
// profile, the same as without one.
func TestStashGetsTheBuiltInProfile(t *testing.T) {
	sub := newSub(t, apps.Policy{NoBuiltin: true})
	const stash = "Stash/3.1.1 Clash/1.9.0"
	builtIn := fetch(t, sub, stash).Body.String()
	sub.cfg.Templates = []model.Template{{ID: 1, Core: model.CoreMihomo, Default: true, Body: ownClash}}
	if w := fetch(t, sub, "mihomo/1.19.31"); !strings.Contains(w.Body.String(), "mine.example") {
		t.Fatalf("a mihomo app gets the template:\n%s", w.Body)
	}
	if w := fetch(t, sub, stash); w.Code != http.StatusOK || w.Body.String() != builtIn {
		t.Fatalf("Stash: %d\n%s", w.Code, w.Body)
	}
}

// Happ and INCY, which take no Clash profile, read their routing of the Clash template all
// the same: the one the Clash profile of the request would be made of.
func TestHappRoutingOfTheClashTemplate(t *testing.T) {
	sub := newSub(t, apps.Policy{NoBuiltin: true})
	sub.cfg.Happ.Routing = xray.HappRoutingAuto
	sub.cfg.Templates = []model.Template{{ID: 1, Core: model.CoreMihomo, Default: true, Body: ownClash}}
	w := fetch(t, sub, "Happ/3.4.1")
	if p := decodeHapp(t, w.Header().Get("routing")); !slices.Contains(p.DirectSites, "domain:mine.example") {
		t.Fatalf("Happ: %+v", p)
	}
	if w := fetch(t, sub, "INCY/2.1.0"); w.Header().Get("routing") == "" || w.Header().Get("routing") != incyAutoRouting(sub.cfg, ownClash) {
		t.Fatalf("INCY: %q", w.Header().Get("routing"))
	}
}

// Config.Profile hands the renderers every setting of the profile; only the app is the
// Clash profile's to set.
func TestConfigProfile(t *testing.T) {
	cfg := Config{Nodes: []model.Node{{ID: 1}}, Direct: []string{"203.0.113.7"}, Rules: []routing.Rule{{}},
		Routes: routing.Routes{Services: map[string]string{"youtube": routing.TargetVPN}}, Lang: "ru"}
	p := reflect.ValueOf(cfg.Profile([]model.Proxy{{Name: "NL"}}))
	for i := range p.NumField() {
		if name := p.Type().Field(i).Name; name != "App" && p.Field(i).IsZero() {
			t.Errorf("%s is not set", name)
		}
	}
}
