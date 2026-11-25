package serve

import (
	"bytes"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"mikan/internal/sub/apps"
	"mikan/internal/sub/model"
)

// previewOf is the preview of a GET of target by an app with the User-Agent ua and the
// headers kv.
func previewOf(sub *fakeSub, target, ua string, kv ...string) Preview {
	r := httptest.NewRequest(http.MethodGet, target, nil)
	if ua != "" {
		r.Header.Set("User-Agent", ua)
	}
	for i := 0; i+1 < len(kv); i += 2 {
		r.Header.Set(kv[i], kv[i+1])
	}
	return handlerOf(sub).Preview(sub, r)
}

// A preview is the answer a fetch gets, the headers and the body byte for byte, and the
// request is not logged.
func TestPreviewIsTheAnswer(t *testing.T) {
	for _, c := range []struct{ target, format string }{
		{"/" + testToken, FormatClash},
		{"/" + testToken + "?format=singbox", FormatSingBox},
		{"/" + testToken + "?format=xray", FormatXray},
		{"/" + testToken + "?format=uri", FormatURI},
	} {
		sub := newSub(t, apps.Policy{NoBuiltin: true})
		r := httptest.NewRequest(http.MethodGet, c.target, nil)
		r.Header.Set("User-Agent", "mihomo/1.19.31")
		w := httptest.NewRecorder()
		handlerOf(sub).Serve(w, r, testToken)
		sub.log = nil
		p := previewOf(sub, c.target, "mihomo/1.19.31")
		if p.Status != w.Code || p.Outcome != OutcomeServed || p.Format != c.format || !bytes.Equal(p.Body, w.Body.Bytes()) {
			t.Errorf("%s: %d %s %q, want %d %s and the fetch's body", c.target, p.Status, p.Outcome, p.Format, w.Code, c.format)
		}
		if !maps.EqualFunc(p.Header, w.Header(), slices.Equal) {
			t.Errorf("%s: headers %v, want %v", c.target, p.Header, w.Header())
		}
		if len(sub.log) != 0 {
			t.Errorf("%s: logged %+v", c.target, sub.log)
		}
		if len(p.Inbounds) == 0 || len(p.Kept) != len(p.Inbounds) || len(p.Served) == 0 || len(p.Skipped) != 0 || p.Stub != "" {
			t.Errorf("%s: %d inbounds, %d kept, %d served, skipped %v, stub %q", c.target, len(p.Inbounds), len(p.Kept), len(p.Served), p.Skipped, p.Stub)
		}
	}
}

// The rule that decided is told with its place, and an app it turns away asks for no keys.
func TestPreviewTellsTheRule(t *testing.T) {
	sub := newSub(t, apps.Policy{NoBuiltin: true, Rules: []apps.Rule{
		rule("drop", "badbot", apps.Action{Respond: apps.RespondDrop}),
		rule("old", "oldapp", apps.Action{Respond: apps.RespondStub}),
	}})
	p := previewOf(sub, "/"+testToken, "OldApp/1")
	if p.Outcome != OutcomeStub || p.Stub != StubUnsupported || p.Decision.Rule != "old" || p.Decision.Position != 2 || p.Status != http.StatusOK {
		t.Errorf("%s %q, rule %q at %d, %d", p.Outcome, p.Stub, p.Decision.Rule, p.Decision.Position, p.Status)
	}
	if len(sub.keys) != 0 || len(sub.log) != 0 {
		t.Errorf("keys asked for %v, logged %+v", sub.keys, sub.log)
	}
}

// A rule that closes the connection is only told: the preview has no answer and goes on.
func TestPreviewOfADroppedRequest(t *testing.T) {
	sub := newSub(t, apps.Policy{NoBuiltin: true, Rules: []apps.Rule{rule("drop", "badbot", apps.Action{Respond: apps.RespondDrop})}})
	p := previewOf(sub, "/"+testToken, "BadBot/1")
	if p.Outcome != OutcomeDropped || p.Status != 0 || p.Body != nil || len(p.Header) != 0 || p.Decision.Rule != "drop" {
		t.Errorf("%s %d %q %v, rule %q", p.Outcome, p.Status, p.Body, p.Header, p.Decision.Rule)
	}
	if len(sub.log) != 0 {
		t.Errorf("logged %+v", sub.log)
	}
}

// A rule on Host sees the host the request names, as with a fetch.
func TestPreviewSeesTheHost(t *testing.T) {
	sub := newSub(t, apps.Policy{NoBuiltin: true, Rules: []apps.Rule{{Name: "old name", Enabled: true, Do: apps.Action{Respond: apps.Respond404},
		When: apps.Group{Op: apps.And, Conds: []apps.Cond{{Header: "Host", Op: apps.OpEquals, Value: "old.example"}}}}}})
	r := httptest.NewRequest(http.MethodGet, "/"+testToken, nil)
	r.Host = "old.example"
	if p := handlerOf(sub).Preview(sub, r); p.Outcome != OutcomeBlocked || p.Status != http.StatusNotFound || p.Decision.Rule != "old name" {
		t.Errorf("%s %d, rule %q", p.Outcome, p.Status, p.Decision.Rule)
	}
	if p := previewOf(sub, "/"+testToken, "Happ/3.6.0"); p.Outcome != OutcomeServed || p.Decision.Rule != "" {
		t.Errorf("another host: %s, rule %q", p.Outcome, p.Decision.Rule)
	}
}

// Why a stub went out in place of the servers.
func TestPreviewTellsTheStub(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{ErrDeviceLimit, StubDeviceLimit},
		{ErrNoHWID, StubNoHWID},
		{ErrDeviceBanned, StubBanned},
		{&UnboundError{Until: testNow.Add(time.Hour)}, StubUnbound},
	} {
		sub := newSub(t, apps.Policy{NoBuiltin: true})
		sub.keysErr = c.err
		if p := previewOf(sub, "/"+testToken, "Happ/3.6.0"); p.Outcome != OutcomeStub || p.Stub != c.want || len(p.Body) == 0 {
			t.Errorf("%v: %s %q, want a stub %q", c.err, p.Outcome, p.Stub, c.want)
		}
	}
	sub := newSub(t, apps.Policy{NoBuiltin: true})
	sub.cfg.InactiveServer = true
	sub.user.State = StateLimited
	if p := previewOf(sub, "/"+testToken, "v2rayNG/1.10.20"); p.Outcome != OutcomeStub || p.Stub != StubInactive {
		t.Errorf("inactive: %s %q", p.Outcome, p.Stub)
	}
}

// An own template that does not render for the client is told, and the built-in profile it
// gave way to is the body.
func TestPreviewOfATemplateThatDoesNotRender(t *testing.T) {
	sub := newSub(t, apps.Policy{NoBuiltin: true})
	sub.groups, sub.pool = testGroups(), 7
	builtIn := previewOf(sub, "/"+testToken, "mihomo/1.19.31", FeaturesHeader, "groups")
	sub.cfg.Templates = []model.Template{{ID: 5, Core: model.CoreMihomo, Default: true,
		Body: "proxy-groups:\n  - {name: VPN, type: select, proxies: [\"🇺🇸 VLESS Vision\"]}\nrules:\n  - MATCH,VPN\n"}}
	p := previewOf(sub, "/"+testToken, "mihomo/1.19.31", FeaturesHeader, "groups")
	if p.Template.ID != 5 || !p.TemplateFailed || p.Outcome != OutcomeServed || !bytes.Equal(p.Body, builtIn.Body) {
		t.Errorf("template %d, failed %v, %s, the built-in body: %v", p.Template.ID, p.TemplateFailed, p.Outcome, bytes.Equal(p.Body, builtIn.Body))
	}
	if builtIn.Template.ID != 0 || builtIn.TemplateFailed {
		t.Errorf("no template: %+v %v", builtIn.Template, builtIn.TemplateFailed)
	}
}
