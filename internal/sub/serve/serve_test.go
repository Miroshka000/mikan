package serve

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"mikan/internal/proto"
	"mikan/internal/sub/apps"
	"mikan/internal/sub/model"
	"mikan/internal/sub/subtest"
)

const testToken = "0123456789abcdefghijklmn"

// fakeSub is a subscription with the servers of the golden files' default user, that
// remembers what it was asked and what the log was told.
type fakeSub struct {
	cfg     Config
	user    User
	keysErr error
	keys    []Device
	log     []LogEntry
}

func (f *fakeSub) Config() Config                       { return f.cfg }
func (f *fakeSub) User(context.Context) (User, error)   { return f.user, nil }
func (f *fakeSub) Bound(context.Context) (int64, error) { return 0, nil }
func (f *fakeSub) Bot(context.Context) string           { return "" }
func (f *fakeSub) Keys(_ context.Context, dev Device) (proto.Slot, error) {
	f.keys = append(f.keys, dev)
	return subtest.Keys, f.keysErr
}
func (f *fakeSub) Inbounds(context.Context) ([]model.Inbound, error) {
	return subtest.Defaults("").Build.Inbounds, nil
}
func (f *fakeSub) Fetched(context.Context, string) {}
func (f *fakeSub) Groups(context.Context, []model.Proxy) (AppGroups, error) {
	return AppGroups{}, nil
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

func handlerOf(sub *fakeSub) *Handler {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	return New(fakeSource{sub}, func() time.Time { return now }, func(*http.Request) string { return "192.0.2.7" })
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
	want := LogEntry{At: time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC), IP: "192.0.2.7", Agent: "Happ/3.4.1", App: "happ", Format: FormatURI, Outcome: OutcomeServed}
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

func TestStubText(t *testing.T) {
	d := apps.Decision{Install: []string{"Happ", "INCY", "Koala Clash"}}
	for _, c := range []struct {
		lang string
		d    apps.Decision
		want string
	}{
		{"en", d, "⛔ This app is not supported — install Happ, INCY or Koala Clash"},
		{"ru", d, "⛔ Это приложение не поддерживается — установите Happ, INCY или Koala Clash"},
		{"en", apps.Decision{Install: []string{"Happ", "INCY"}}, "⛔ This app is not supported — install Happ or INCY"},
		{"en", apps.Decision{Install: []string{"Happ"}}, "⛔ This app is not supported — install Happ"},
		{"en", apps.Decision{}, "⛔ This app is not supported — open the subscription link in a browser"},
		{"ru", apps.Decision{}, "⛔ Это приложение не поддерживается — откройте ссылку подписки в браузере"},
		{"en", apps.Decision{Action: apps.Action{Text: " Use Happ,\r\nit is free "}, Install: d.Install}, "Use Happ,  it is free"},
	} {
		if got := StubText(c.lang, c.d); got != c.want {
			t.Errorf("%s: %q, want %q", c.lang, got, c.want)
		}
	}
}

func TestResolve(t *testing.T) {
	h := http.Header{"User-Agent": {"Happ/3.4.1"}}
	if got := Resolve(h, "", apps.Decision{}); got != FormatURI {
		t.Errorf("%s", got)
	}
	if got := Resolve(h, "clash", apps.Decision{}); got != FormatClash {
		t.Errorf("?format=: %s", got)
	}
	if got := Resolve(h, "clash", apps.Decision{Action: apps.Action{Format: FormatSingBox}}); got != FormatSingBox {
		t.Errorf("a rule's format: %s", got)
	}
}
