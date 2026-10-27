package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"mikan/internal/logbuf"
	"mikan/internal/nodeapi"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/support"
	"mikan/internal/panel/tgate"
	"mikan/internal/proto"
	"mikan/internal/release"
)

// supportHarness is a logged-in admin with a read key and a full key, plus the means to be
// a support operator in a browser of its own.
type supportHarness struct {
	*keyHarness
	bot *fakeSupportBot
}

// fakeSupportBot is the support bot: any well-formed code is of ticket 17 unless told
// otherwise, and the link the panel hands over is kept.
type fakeSupportBot struct {
	mu        sync.Mutex
	claimErr  error
	attachErr error
	claims    []string
	links     []string
}

func (b *fakeSupportBot) Claim(_ context.Context, code string) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.claims = append(b.claims, code)
	if b.claimErr != nil {
		return 0, b.claimErr
	}
	return 17, nil
}

func (b *fakeSupportBot) Attach(_ context.Context, code, link string) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.attachErr != nil {
		return 0, b.attachErr
	}
	b.links = append(b.links, link)
	return 17, nil
}

func (b *fakeSupportBot) lastLink() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.links) == 0 {
		return ""
	}
	return b.links[len(b.links)-1]
}

type grantReply struct {
	Grant struct {
		ID              int64    `json:"id"`
		Level           string   `json:"level"`
		ShowPeople      bool     `json:"show_people"`
		Extended        bool     `json:"extended"`
		TerminalTargets []string `json:"terminal_targets"`
		Status          string   `json:"status"`
	} `json:"grant"`
	Ticket int64 `json:"ticket"`
	// What the bot got: the link and its token. The admin is never shown them.
	Token string `json:"-"`
	Link  string `json:"-"`
}

func newSupportHarness(t *testing.T, with ...func(*Options)) *supportHarness {
	t.Helper()
	bot := &fakeSupportBot{}
	with = append([]func(*Options){func(o *Options) { o.SupportBot = bot }}, with...)
	return &supportHarness{newKeyHarness(t, with...), bot}
}

// grant opens an access the way the admin does: from the session, with a code from the bot
// (a good one unless the body has its own). The link the bot got is in the reply.
func (s *supportHarness) grant(body map[string]any) (grantReply, int, []byte) {
	s.t.Helper()
	if _, ok := body["code"]; !ok {
		body["code"] = "ABCD2345"
	}
	resp, out := s.do(http.MethodPost, s.api+"/support/grants", body, s.csrf)
	var g grantReply
	if resp.StatusCode == http.StatusCreated {
		if err := json.Unmarshal(out, &g); err != nil {
			s.t.Fatalf("grant reply: %v %s", err, out)
		}
		g.Link = s.bot.lastLink()
		if i := strings.LastIndex(g.Link, "#"); i >= 0 {
			g.Token = g.Link[i+1:]
		}
	}
	return g, resp.StatusCode, out
}

// operator is a browser that has no part of the admin's session.
type operator struct {
	s      *supportHarness
	client *http.Client
	csrf   string
}

func (s *supportHarness) newOperator() *operator {
	c := *s.ts.Client()
	c.Jar, _ = cookiejar.New(nil)
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &operator{s: s, client: &c}
}

func (o *operator) do(method, path string, body any, hdr map[string]string) (*http.Response, []byte) {
	o.s.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, o.s.ts.URL+o.s.api+path, rd)
	if err != nil {
		o.s.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := o.client.Do(req)
	if err != nil {
		o.s.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, out
}

// enter takes the link's token into the operator's browser.
func (o *operator) enter(token string) (*http.Response, []byte) {
	resp, out := o.do(http.MethodPost, "/support/enter", map[string]any{"token": token}, nil)
	if resp.StatusCode == http.StatusOK {
		var w struct {
			CSRF string `json:"csrf_token"`
		}
		_ = json.Unmarshal(out, &w)
		o.csrf = w.CSRF
	}
	return resp, out
}

func (o *operator) write(method, path string, body any) (*http.Response, []byte) {
	return o.do(method, path, body, map[string]string{"X-CSRF-Token": o.csrf})
}

func bearerOf(token string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + token}
}

func detail(body []byte) string {
	var p struct {
		Detail string `json:"detail"`
	}
	_ = json.Unmarshal(body, &p)
	return p.Detail
}

// The admin opens an access; the operator enters once by the link, then by token for as long
// as it lives; the admin sees it, and ends it at once.
func TestSupportAccessLifecycle(t *testing.T) {
	s := newSupportHarness(t)

	g, code, out := s.grant(map[string]any{"hours": 2, "level": "view", "show_people": true, "note": "ticket 17"})
	if code != http.StatusCreated {
		t.Fatalf("grant: %d %s", code, out)
	}
	if !strings.HasPrefix(g.Token, "ms_") || len(g.Token) != 43 {
		t.Fatalf("token: %q", g.Token)
	}
	if want := "https://203.0.113.10:21355/" + adminPath + "/support#" + g.Token; g.Link != want {
		t.Fatalf("link %q, want %q", g.Link, want)
	}
	// The admin gets the ticket, never the link.
	if g.Ticket != 17 || strings.Contains(string(out), g.Token) || strings.Contains(string(out), "/support#") {
		t.Fatalf("reply: %s", out)
	}
	// Only the hash is stored.
	var stored string
	if err := s.st.DB.QueryRow("SELECT token_hash FROM support_grants WHERE id = $1", g.Grant.ID).Scan(&stored); err != nil || stored == g.Token || strings.Contains(stored, "ms_") {
		t.Fatalf("stored %q %v", stored, err)
	}
	// The list never shows the token again.
	resp, body := s.do(http.MethodGet, s.api+"/support/grants", nil, nil)
	if resp.StatusCode != http.StatusOK || strings.Contains(string(body), g.Token) || !strings.Contains(string(body), `"status":"active"`) || !strings.Contains(string(body), "support.grant") {
		t.Fatalf("list: %d %s", resp.StatusCode, body)
	}

	op := s.newOperator()
	if resp, body := op.do(http.MethodGet, "/support/whoami", nil, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("whoami before entering: %d %s", resp.StatusCode, body)
	}
	resp, body = op.enter(g.Token)
	if resp.StatusCode != http.StatusOK || op.csrf == "" {
		t.Fatalf("enter: %d %s", resp.StatusCode, body)
	}
	var w struct {
		Level        string `json:"level"`
		ExpiresAt    string `json:"expires_at"`
		ShowPeople   bool   `json:"show_people"`
		PanelVersion string `json:"panel_version"`
		ReleaseKey   string `json:"release_key"`
		Nodes        []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(body, &w); err != nil || w.Level != "view" || !w.ShowPeople || w.PanelVersion != "test" || w.ExpiresAt == "" || w.ReleaseKey != release.KeyFingerprint() {
		t.Fatalf("enter body: %v %s", err, body)
	}
	// The one entry of the link is spent, in this browser and in any other.
	for _, o := range []*operator{op, s.newOperator()} {
		if resp, body := o.enter(g.Token); resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "support_link_used") {
			t.Fatalf("second entry: %d %s", resp.StatusCode, body)
		}
	}
	// The session and the token both work.
	if resp, body := op.do(http.MethodGet, "/support/whoami", nil, nil); resp.StatusCode != http.StatusOK || !strings.Contains(string(body), op.csrf) {
		t.Fatalf("whoami by session: %d %s", resp.StatusCode, body)
	}
	bare := s.newOperator()
	if resp, body := bare.do(http.MethodGet, "/support/whoami", nil, bearerOf(g.Token)); resp.StatusCode != http.StatusOK || strings.Contains(string(body), "csrf_token") {
		t.Fatalf("whoami by token: %d %s", resp.StatusCode, body)
	}
	// Not an operator: the admin's session and a key get nothing from whoami.
	if resp, body := s.do(http.MethodGet, s.api+"/support/whoami", nil, nil); resp.StatusCode != http.StatusForbidden || detail(body) != "support_only" {
		t.Fatalf("whoami as the admin: %d %s", resp.StatusCode, body)
	}
	if resp, body := s.asKey(s.full, http.MethodGet, "/support/whoami", nil); resp.StatusCode != http.StatusForbidden || detail(body) != "support_only" {
		t.Fatalf("whoami with a key: %d %s", resp.StatusCode, body)
	}
	// The operator's browser is not among the admin's sessions.
	_, body = s.do(http.MethodGet, s.api+"/auth/sessions", nil, nil)
	var sessions []map[string]any
	if err := json.Unmarshal(body, &sessions); err != nil || len(sessions) != 1 {
		t.Fatalf("the admin's sessions: %v %s", err, body)
	}

	// The journal has the grant and the entry; the card shows the operator was there.
	_, body = s.do(http.MethodGet, s.api+"/support/grants", nil, nil)
	for _, want := range []string{"support.grant", "support.enter", `"last_used_at":"`, `"entered_at":"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the card lacks %s: %s", want, body)
		}
	}

	// Revoked: the browser and the token both lose it at once, and nobody enters again.
	if resp, body := s.do(http.MethodDelete, s.api+"/support/grants/"+idOf(g.Grant.ID), nil, s.csrf); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", resp.StatusCode, body)
	}
	if resp, _ := op.do(http.MethodGet, "/users", nil, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a revoked session: %d", resp.StatusCode)
	}
	if resp, _ := bare.do(http.MethodGet, "/users", nil, bearerOf(g.Token)); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a revoked token: %d", resp.StatusCode)
	}
	if resp, body := s.newOperator().enter(g.Token); resp.StatusCode != http.StatusUnauthorized || !strings.Contains(string(body), "invalid_support_token") {
		t.Fatalf("entering a revoked grant: %d %s", resp.StatusCode, body)
	}
	var left int
	if err := s.st.DB.QueryRow("SELECT count(*) FROM sessions WHERE support_grant_id IS NOT NULL").Scan(&left); err != nil || left != 0 {
		t.Fatalf("the operator's sessions stay: %d %v", left, err)
	}
	if resp, _ := s.do(http.MethodDelete, s.api+"/support/grants/"+idOf(g.Grant.ID), nil, s.csrf); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("revoking twice: %d", resp.StatusCode)
	}
	for _, action := range []string{"support.grant", "support.enter", "support.revoke"} {
		if n := auditCount(t, s.harness, action); n != 1 {
			t.Errorf("%s written %d times", action, n)
		}
	}
}

func auditCount(t *testing.T, h *harness, action string) int {
	t.Helper()
	rows, err := h.st.Q.ListAudit(context.Background(), db.ListAuditParams{BeforeID: 1 << 62, Lim: 500})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range rows {
		if r.Action == action {
			n++
		}
	}
	return n
}

// A grant ends by itself: the browser and the token stop at its time, the journal says it
// expired once, and the one browser entry of a link nobody used is gone too.
func TestSupportAccessExpires(t *testing.T) {
	s := newSupportHarness(t)
	g, code, out := s.grant(map[string]any{"hours": 2, "level": "view"})
	if code != http.StatusCreated {
		t.Fatalf("grant: %d %s", code, out)
	}
	op := s.newOperator()
	if resp, body := op.enter(g.Token); resp.StatusCode != http.StatusOK {
		t.Fatalf("enter: %d %s", resp.StatusCode, body)
	}
	spare, _, _ := s.grant(map[string]any{"hours": 2, "level": "view"}) // replaces the first
	// The first grant was replaced: the browser is out although its time has not run out.
	if resp, _ := op.do(http.MethodGet, "/users", nil, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a replaced grant's session: %d", resp.StatusCode)
	}
	if resp, _ := s.newOperator().do(http.MethodGet, "/users", nil, bearerOf(g.Token)); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a replaced grant's token: %d", resp.StatusCode)
	}
	live := s.newOperator()
	if resp, _ := live.do(http.MethodGet, "/users", nil, bearerOf(spare.Token)); resp.StatusCode != http.StatusOK {
		t.Fatalf("the new grant's token: %d", resp.StatusCode)
	}
	// Two hours and a minute on.
	s.advance(2*time.Hour + time.Minute)
	if resp, _ := live.do(http.MethodGet, "/users", nil, bearerOf(spare.Token)); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an expired token: %d", resp.StatusCode)
	}
	if resp, body := s.newOperator().enter(spare.Token); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("entering an expired grant: %d %s", resp.StatusCode, body)
	}
	if err := s.p.Support.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := auditCount(t, s.harness, "support.expire"); n != 1 {
		t.Fatalf("support.expire written %d times", n)
	}
	if n := auditCount(t, s.harness, "support.revoke"); n != 1 {
		t.Fatalf("support.revoke (the replaced grant) written %d times", n)
	}
	// The card still says what it was.
	_, body := s.do(http.MethodGet, s.api+"/support/grants", nil, nil)
	if !strings.Contains(string(body), `"status":"expired"`) {
		t.Fatalf("card: %s", body)
	}
}

// Everything the plan closes stays closed to an operator, at every level, in a browser and
// with a token; a view access cannot write anywhere else either.
func TestSupportOperatorBoundaries(t *testing.T) {
	s := newSupportHarness(t)
	closed := []struct{ method, path string }{
		{http.MethodGet, "/auth/me"}, {http.MethodGet, "/auth/sessions"}, {http.MethodPost, "/auth/password"},
		{http.MethodPost, "/auth/totp/setup"}, {http.MethodPost, "/auth/totp/disable"}, {http.MethodPost, "/auth/logout"},
		{http.MethodGet, "/api-keys"}, {http.MethodPost, "/api-keys"}, {http.MethodDelete, "/api-keys/1"},
		{http.MethodGet, "/support/grants"}, {http.MethodPost, "/support/grants"}, {http.MethodDelete, "/support/grants/1"},
		{http.MethodPost, "/settings/reset-admin-path"},
		{http.MethodGet, "/telegram/backup"}, {http.MethodPatch, "/telegram/backup"}, {http.MethodPost, "/telegram/backup/send"},
		{http.MethodGet, "/audit"}, {http.MethodPost, "/telegram/broadcast"},
		// The bot, its route and the admin's chat for notices: an operator who could mute or
		// swap them would enter and open a terminal unseen.
		{http.MethodPatch, "/telegram"}, {http.MethodPost, "/telegram/infrastructure/connect"}, {http.MethodDelete, "/telegram/infrastructure/connect"},
	}
	for _, level := range []string{"view", "change"} {
		g, code, out := s.grant(map[string]any{"hours": 6, "level": level})
		if code != http.StatusCreated {
			t.Fatalf("%s grant: %d %s", level, code, out)
		}
		op := s.newOperator()
		if resp, body := op.enter(g.Token); resp.StatusCode != http.StatusOK {
			t.Fatalf("enter: %d %s", resp.StatusCode, body)
		}
		by := map[string]func(method, path string, body any) (*http.Response, []byte){
			"browser": func(m, p string, b any) (*http.Response, []byte) { return op.write(m, p, b) },
			"token": func(m, p string, b any) (*http.Response, []byte) {
				return s.newOperator().do(m, p, b, bearerOf(g.Token))
			},
		}
		for how, call := range by {
			for _, c := range closed {
				resp, body := call(c.method, c.path, map[string]any{})
				if resp.StatusCode != http.StatusForbidden || detail(body) != "support_denied" {
					t.Errorf("%s %s %s %s: %d %s, want 403 support_denied", level, how, c.method, c.path, resp.StatusCode, body)
				}
			}
			// Reading the rest works at both levels.
			if resp, body := call(http.MethodGet, "/users", nil); resp.StatusCode != http.StatusOK {
				t.Errorf("%s %s read: %d %s", level, how, resp.StatusCode, body)
			}
			resp, body := call(http.MethodPatch, "/settings", map[string]any{"brand": "Support-" + level})
			switch level {
			case "view":
				if resp.StatusCode != http.StatusForbidden || detail(body) != "support_read_only" {
					t.Errorf("view %s write: %d %s, want 403 support_read_only", how, resp.StatusCode, body)
				}
			case "change":
				if resp.StatusCode != http.StatusOK {
					t.Errorf("change %s write: %d %s", how, resp.StatusCode, body)
				}
			}
		}
		// A view access is refused on any method that changes.
		if level == "view" {
			for _, m := range []string{http.MethodPatch, http.MethodDelete} {
				if resp, body := op.write(m, "/users/1", map[string]any{}); resp.StatusCode != http.StatusForbidden || detail(body) != "support_read_only" {
					t.Errorf("view %s /users/1: %d %s", m, resp.StatusCode, body)
				}
			}
		}
	}
	// What the operator changed carries the grant, not the admin.
	rows, err := s.st.Q.ListAudit(context.Background(), db.ListAuditParams{BeforeID: 1 << 62, Lim: 200})
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, r := range rows {
		if r.Action == "settings.update" && strings.Contains(r.Details.String, `"actor":"support:`) {
			seen++
			if r.AdminID.Valid {
				t.Errorf("an operator's line names the admin: %+v", r)
			}
		}
	}
	if seen != 2 {
		t.Errorf("the operator's settings.update lines: %d, want 2 (browser and token)", seen)
	}
	// The card lists them.
	_, body := s.do(http.MethodGet, s.api+"/support/grants", nil, nil)
	if !strings.Contains(string(body), "settings.update") {
		t.Errorf("the card lacks the operator's actions: %s", body)
	}
}

// A grant is made by the admin's session alone: not by a key, and not by an operator.
func TestSupportGrantNeedsTheAdminSession(t *testing.T) {
	s := newSupportHarness(t)
	for name, key := range map[string]string{"read key": s.read, "full key": s.full} {
		for _, c := range []struct{ method, path string }{{http.MethodPost, "/support/grants"}, {http.MethodGet, "/support/grants"}, {http.MethodDelete, "/support/grants/1"}} {
			resp, body := s.asKey(key, c.method, c.path, map[string]any{"hours": 2, "level": "view"})
			if resp.StatusCode != http.StatusForbidden || detail(body) != "session_only" {
				t.Errorf("%s %s %s: %d %s", name, c.method, c.path, resp.StatusCode, body)
			}
		}
	}
	if n := countGrants(t, s); n != 0 {
		t.Fatalf("a key made %d grants", n)
	}
	// No session: nothing either.
	anon := s.newOperator()
	if resp, _ := anon.do(http.MethodPost, "/support/grants", map[string]any{"hours": 2, "level": "view"}, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d", resp.StatusCode)
	}
	// The session needs its CSRF header.
	if resp, _ := s.do(http.MethodPost, s.api+"/support/grants", map[string]any{"hours": 2, "level": "view"}, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("no csrf: %d", resp.StatusCode)
	}
	// The form's own limits.
	for name, body := range map[string]map[string]any{
		"hours":       {"hours": 3, "level": "view"},
		"level":       {"hours": 2, "level": "root"},
		"note":        {"hours": 2, "level": "view", "note": strings.Repeat("x", 201)},
		"view+target": {"hours": 2, "level": "view", "terminal_targets": []string{"panel"}},
	} {
		if _, code, out := s.grant(body); code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s", name, code, out)
		}
	}
	if n := countGrants(t, s); n != 0 {
		t.Fatalf("a bad form made %d grants", n)
	}
}

func countGrants(t *testing.T, s *supportHarness) int {
	t.Helper()
	var n int
	if err := s.st.DB.QueryRow("SELECT count(*) FROM support_grants").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The entry is a POST like any: a request from another site is refused, and guessing tokens
// blocks the address.
func TestSupportEnterIsGuarded(t *testing.T) {
	s := newSupportHarness(t)
	g, code, out := s.grant(map[string]any{"hours": 2, "level": "view"})
	if code != http.StatusCreated {
		t.Fatalf("grant: %d %s", code, out)
	}
	op := s.newOperator()
	for name, hdr := range map[string]map[string]string{
		"cross site":   {"Sec-Fetch-Site": "cross-site"},
		"other origin": {"Origin": "https://evil.example"},
	} {
		resp, body := op.do(http.MethodPost, "/support/enter", map[string]any{"token": g.Token}, hdr)
		if resp.StatusCode != http.StatusForbidden || detail(body) != "csrf" {
			t.Errorf("%s: %d %s", name, resp.StatusCode, body)
		}
	}
	// The refused entries did not spend the link.
	if resp, body := op.enter(g.Token); resp.StatusCode != http.StatusOK {
		t.Fatalf("enter after refused ones: %d %s", resp.StatusCode, body)
	}
	// Tokens of the wrong shape and wrong tokens of the right shape.
	guess := s.newOperator()
	for _, bad := range []string{"", "ms_", "mk_" + strings.Repeat("a", 40), "ms_" + strings.Repeat("a", 40), strings.Repeat("z", 43)} {
		resp, _ := guess.do(http.MethodPost, "/support/enter", map[string]any{"token": bad}, nil)
		if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("token %q: %d", bad, resp.StatusCode)
		}
	}
	for range 12 {
		guess.do(http.MethodPost, "/support/enter", map[string]any{"token": "ms_" + strings.Repeat("b", 40)}, nil)
	}
	if resp, _ := guess.do(http.MethodPost, "/support/enter", map[string]any{"token": "ms_" + strings.Repeat("c", 40)}, nil); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("after guessing: %d", resp.StatusCode)
	}
	// Bearer guesses are counted too, and a malformed ms_ token is no API key.
	other := s.newOperator()
	for _, bad := range []string{"ms_short", "ms_" + strings.Repeat("x", 40), "ms_" + strings.Repeat("x", 4000)} {
		if resp, _ := other.do(http.MethodGet, "/users", nil, bearerOf(bad)); resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("bearer %q: %d", bad[:min(10, len(bad))], resp.StatusCode)
		}
	}
}

// The terminal level is the admin's decision twice over: it asks for the password again, it
// needs servers, and the servers must exist. The grant keeps them for the terminal to check.
func TestSupportTerminalGrant(t *testing.T) {
	s := newSupportHarness(t)
	var node int64
	if err := s.st.DB.QueryRow("INSERT INTO nodes(name,address,created_at,updated_at) VALUES('remote','198.51.100.7:9000',1,1) RETURNING id").Scan(&node); err != nil {
		t.Fatal(err)
	}
	target := "node:" + idOf(node)
	base := func(extra map[string]any) map[string]any {
		b := map[string]any{"hours": 2, "level": "terminal", "terminal_targets": []string{"panel", target}}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}
	// Without the password, and with a wrong one: no grant.
	if _, code, out := s.grant(base(nil)); code != http.StatusUnprocessableEntity || !strings.Contains(string(out), "wrong_password") {
		t.Fatalf("no password: %d %s", code, out)
	}
	if _, code, out := s.grant(base(map[string]any{"password": "not-the-password"})); code != http.StatusUnprocessableEntity || !strings.Contains(string(out), "wrong_password") {
		t.Fatalf("wrong password: %d %s", code, out)
	}
	if n := countGrants(t, s); n != 0 {
		t.Fatalf("%d grants without the password", n)
	}
	for name, targets := range map[string][]string{
		"none": {}, "unknown node": {"node:99999"}, "junk": {"root"}, "padded": {"node:" + "0" + idOf(node)}, "signed": {"node:+" + idOf(node)}, "zero": {"node:0"},
	} {
		_, code, out := s.grant(base(map[string]any{"password": password, "terminal_targets": targets}))
		if code != http.StatusUnprocessableEntity || !strings.Contains(string(out), "terminal_targets") {
			t.Errorf("targets %s: %d %s", name, code, out)
		}
	}
	g, code, out := s.grant(base(map[string]any{"password": password, "terminal_targets": []string{"panel", target, "panel"}}))
	if code != http.StatusCreated || g.Grant.Level != "terminal" || strings.Join(g.Grant.TerminalTargets, ",") != "panel,"+target {
		t.Fatalf("terminal grant: %d %s", code, out)
	}
	// An operator with it writes like a "change" access and has its servers on whoami.
	op := s.newOperator()
	if resp, body := op.enter(g.Token); resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"terminal_targets":["panel","`+target+`"]`) {
		t.Fatalf("enter: %d %s", resp.StatusCode, body)
	}
	if resp, body := op.write(http.MethodPatch, "/settings", map[string]any{"brand": "T"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("a terminal access writes: %d %s", resp.StatusCode, body)
	}
}

// What outlasts a grant or moves money (a node's key, its address, the payment details, the
// bot, the page every subscriber opens) is closed to a change access, by operation and by
// field, and open in the extended mode, which takes the password like a terminal; what no
// operator reaches stays closed in it.
func TestSupportExtendedMode(t *testing.T) {
	s := newSupportHarness(t)
	var node int64
	if err := s.st.DB.QueryRow("INSERT INTO nodes(name,address,public_host,created_at,updated_at) VALUES('remote','198.51.100.7:9000','198.51.100.7',1,1) RETURNING id").Scan(&node); err != nil {
		t.Fatal(err)
	}
	nodePath := "/nodes/" + idOf(node)
	g, code, out := s.grant(map[string]any{"hours": 2, "level": "change"})
	if code != http.StatusCreated || g.Grant.Extended {
		t.Fatalf("change grant: %d %s", code, out)
	}
	op := s.newOperator()
	if resp, body := op.enter(g.Token); resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"extended":false`) {
		t.Fatalf("enter: %d %s", resp.StatusCode, body)
	}
	extendedOnly := []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/nodes", map[string]any{"name": "mine", "host": "203.0.113.50"}},
		{http.MethodPost, nodePath + "/key", nil},
		{http.MethodPatch, "/payments/settings", map[string]any{}},
		{http.MethodPost, "/balances/42/adjust", map[string]any{"amount": 100}},
		{http.MethodPut, "/telegram/bot-addon", map[string]any{}},
		{http.MethodPut, "/exit-dns", map[string]any{}},
		{http.MethodPut, "/sub-page", map[string]any{}},
		// By field: the rest of the endpoint stays the operator's.
		{http.MethodPatch, nodePath, map[string]any{"name": "remote", "host": "198.51.100.8", "domain": ""}},
		{http.MethodPatch, nodePath, map[string]any{"name": "remote", "host": "198.51.100.7", "domain": "evil.example"}},
		{http.MethodPatch, "/settings", map[string]any{"sub_title": "Mine"}},
	}
	for _, c := range extendedOnly {
		if resp, body := op.write(c.method, c.path, c.body); resp.StatusCode != http.StatusForbidden || detail(body) != "support_extended" {
			t.Errorf("change %s %s: %d %s, want 403 support_extended", c.method, c.path, resp.StatusCode, body)
		}
	}
	// The node's form sends its address and domain as they are: that edit goes through.
	if resp, body := op.write(http.MethodPatch, nodePath, map[string]any{"name": "remote", "host": "198.51.100.7", "domain": "", "enabled": false}); resp.StatusCode != http.StatusOK {
		t.Fatalf("change edits a node: %d %s", resp.StatusCode, body)
	}
	if resp, body := op.write(http.MethodPost, "/system/restart", map[string]any{}); detail(body) == "support_extended" || detail(body) == "support_denied" {
		t.Fatalf("a restart is the change level's: %d %s", resp.StatusCode, body)
	}

	// The mode takes the password, and a level that writes.
	if _, code, out := s.grant(map[string]any{"hours": 2, "level": "change", "extended": true}); code != http.StatusUnprocessableEntity || !strings.Contains(string(out), "wrong_password") {
		t.Fatalf("extended without the password: %d %s", code, out)
	}
	if _, code, out := s.grant(map[string]any{"hours": 2, "level": "view", "extended": true, "password": password}); code != http.StatusUnprocessableEntity || !strings.Contains(string(out), "support_extended_level") {
		t.Fatalf("extended view: %d %s", code, out)
	}
	g, code, out = s.grant(map[string]any{"hours": 2, "level": "change", "extended": true, "password": password})
	if code != http.StatusCreated || !g.Grant.Extended {
		t.Fatalf("extended grant: %d %s", code, out)
	}
	ext := s.newOperator()
	if resp, body := ext.enter(g.Token); resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"extended":true`) {
		t.Fatalf("enter extended: %d %s", resp.StatusCode, body)
	}
	if resp, body := ext.write(http.MethodPatch, nodePath, map[string]any{"name": "remote", "host": "198.51.100.8", "domain": ""}); resp.StatusCode != http.StatusOK {
		t.Fatalf("extended moves a node: %d %s", resp.StatusCode, body)
	}
	var host string
	if err := s.st.DB.QueryRow("SELECT public_host FROM nodes WHERE id = $1", node).Scan(&host); err != nil || host != "198.51.100.8" {
		t.Fatalf("node host %q %v", host, err)
	}
	if resp, body := ext.write(http.MethodPatch, "/settings", map[string]any{"sub_title": "Support"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("extended writes a session field: %d %s", resp.StatusCode, body)
	}
	// Past the gate: a node without a host is the form's own refusal, not the gate's.
	if resp, body := ext.write(http.MethodPost, "/nodes", map[string]any{"name": "x"}); resp.StatusCode == http.StatusForbidden {
		t.Fatalf("extended adds a node: %d %s", resp.StatusCode, body)
	}
	for _, c := range []struct{ method, path string }{
		{http.MethodPatch, "/telegram"}, {http.MethodGet, "/audit"}, {http.MethodGet, "/support/grants"}, {http.MethodPost, "/support/grants"},
		{http.MethodPost, "/settings/reset-admin-path"}, {http.MethodGet, "/api-keys"}, {http.MethodPost, "/auth/password"},
	} {
		if resp, body := ext.write(c.method, c.path, map[string]any{}); resp.StatusCode != http.StatusForbidden || detail(body) != "support_denied" {
			t.Errorf("extended %s %s: %d %s, want 403 support_denied", c.method, c.path, resp.StatusCode, body)
		}
	}
}

// The panel's secrets stay out of an operator's answers at any level; the people stay out
// unless the admin let the operator see them.
func TestSupportOperatorSeesNoSecretsAndMaybeNoPeople(t *testing.T) {
	s := newSupportHarness(t)
	ctx := context.Background()
	tariffs, _ := s.st.Q.ListTariffs(ctx)
	clock := func() time.Time { return s.now }
	u, err := domain.NewUsers(s.st, domain.NewPool(s.st, clock), noChanges{}, clock).Create(ctx, domain.CreateInput{Name: "Ivan Petrov", Contact: "@ivan_contact", Note: "owes a month", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.DB.Exec("INSERT INTO tg_chats(tg_id, username, first_name, created_at, updated_at) VALUES (4242424, 'ivan_tg', 'Ivan', 1, 1)"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.DB.Exec("INSERT INTO tg_links(user_id, tg_id, created_at) VALUES ($1, 4242424, 1)", u.ID); err != nil {
		t.Fatal(err)
	}
	// An address the subscriber came from: the list of devices is a bare array of them.
	if _, err := s.st.DB.Exec("INSERT INTO devices(user_id, ip, first_seen, last_seen) VALUES ($1, '198.18.0.77', 1, 2)", u.ID); err != nil {
		t.Fatal(err)
	}
	people := []string{"Ivan Petrov", "ivan_contact", "owes a month", u.SubToken, "ivan_tg", "4242424", "198.18.0.77"}
	paths := []string{"/users", "/users/" + idOf(u.ID), "/users/" + idOf(u.ID) + "/devices", "/settings"}

	hidden, code, out := s.grant(map[string]any{"hours": 2, "level": "change"})
	if code != http.StatusCreated {
		t.Fatalf("grant: %d %s", code, out)
	}
	for how, call := range map[string]func(path string) (*http.Response, []byte){
		"token": func(p string) (*http.Response, []byte) {
			return s.newOperator().do(http.MethodGet, p, nil, bearerOf(hidden.Token))
		},
	} {
		for _, p := range paths {
			resp, body := call(p)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("%s %s: %d %s", how, p, resp.StatusCode, body)
			}
			for _, secret := range append(people, adminPath, subPath) {
				if strings.Contains(string(body), secret) {
					t.Errorf("%s %s leaks %q: %s", how, p, secret, body)
				}
			}
		}
		// The shape stays: the user is there, by number.
		_, body := call("/users/" + idOf(u.ID))
		if !strings.Contains(string(body), `"name":"#`+idOf(u.ID)+`"`) {
			t.Errorf("a masked user keeps no number: %s", body)
		}
	}
	// A search is an oracle on the people behind the mask (the total and the ids it finds say
	// what a name, a contact, a note or a tag holds): closed to this operator, whatever the
	// text, in a browser and with a token alike. An empty or blank one is no search.
	for _, q := range []string{"@ivan_contact", "a", "ivan", "owes", "%20"} {
		resp, body := s.newOperator().do(http.MethodGet, "/users?q="+q, nil, bearerOf(hidden.Token))
		blank := strings.TrimSpace(strings.ReplaceAll(q, "%20", " ")) == ""
		if blank && resp.StatusCode != http.StatusOK || !blank && (resp.StatusCode != http.StatusForbidden || detail(body) != "support_denied") {
			t.Errorf("a masked operator searches %q: %d %s", q, resp.StatusCode, body)
		}
	}
	// A writing answer is masked too: the card the operator gets back after an edit.
	resp, body := s.newOperator().do(http.MethodPatch, "/users/"+idOf(u.ID), map[string]any{"note": "from support"}, bearerOf(hidden.Token))
	if resp.StatusCode != http.StatusOK || strings.Contains(string(body), "from support") || strings.Contains(string(body), "Ivan") {
		t.Errorf("edit answer: %d %s", resp.StatusCode, body)
	}
	// The link of a subscriber is the credential: no happ link for a masked operator.
	if resp, body := s.newOperator().do(http.MethodGet, "/users/"+idOf(u.ID)+"/happ-link", nil, bearerOf(hidden.Token)); resp.StatusCode != http.StatusForbidden {
		t.Errorf("happ link: %d %s", resp.StatusCode, body)
	}

	// Let the operator see the people: names, contacts and links come, the panel's secrets do not.
	shown, code, out := s.grant(map[string]any{"hours": 2, "level": "view", "show_people": true})
	if code != http.StatusCreated {
		t.Fatalf("grant: %d %s", code, out)
	}
	_, body = s.newOperator().do(http.MethodGet, "/users/"+idOf(u.ID), nil, bearerOf(shown.Token))
	for _, want := range []string{"Ivan Petrov", "ivan_contact", u.SubToken} {
		if !strings.Contains(string(body), want) {
			t.Errorf("a shown operator misses %q: %s", want, body)
		}
	}
	// ... and may search them, and sees the addresses.
	if resp, body := s.newOperator().do(http.MethodGet, "/users?q=ivan_contact", nil, bearerOf(shown.Token)); resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Ivan Petrov") {
		t.Errorf("a shown operator's search: %d %s", resp.StatusCode, body)
	}
	if _, body := s.newOperator().do(http.MethodGet, "/users/"+idOf(u.ID)+"/devices", nil, bearerOf(shown.Token)); !strings.Contains(string(body), "198.18.0.77") {
		t.Errorf("a shown operator misses the addresses: %s", body)
	}
	_, body = s.newOperator().do(http.MethodGet, "/settings", nil, bearerOf(shown.Token))
	if strings.Contains(string(body), adminPath) || strings.Contains(string(body), subPath) {
		t.Errorf("a shown operator gets the secret paths: %s", body)
	}
	// The admin's own answers are untouched.
	_, body = s.do(http.MethodGet, s.api+"/users/"+idOf(u.ID), nil, nil)
	if !strings.Contains(string(body), "Ivan Petrov") || !strings.Contains(string(body), u.SubToken) {
		t.Errorf("the admin lost the people: %s", body)
	}
}

// The admin is told in Telegram when the operator comes and when the access ends, once
// each; without the bot nothing is sent and nothing breaks.
func TestSupportAdminNotices(t *testing.T) {
	var mu sync.Mutex
	var sent []string
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			var m struct {
				ChatID int64  `json:"chat_id"`
				Text   string `json:"text"`
			}
			_ = json.Unmarshal(raw, &m)
			mu.Lock()
			sent = append(sent, strconv.FormatInt(m.ChatID, 10)+"|"+m.Text)
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":1}}`)
	}))
	t.Cleanup(tg.Close)

	bot := &fakeSupportBot{}
	h := newHarness(t, func(o *Options) { o.TelegramAPI = tg.URL; o.SupportBot = bot })
	ctx := context.Background()
	set := settings.New(h.st.Q)
	for k, v := range map[string]any{tgate.KeyToken: "123:abc", tgate.KeyEnabled: true, tgate.KeyInfraAdminChat: int64(777), settings.KeyPublicHost: "203.0.113.10", settings.KeyPanelPort: 21355} {
		if err := settings.Set(ctx, set, k, v); err != nil {
			t.Fatal(err)
		}
	}
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	k := &keyHarness{harness: h, api: "/" + adminPath + "/api/v1", csrf: map[string]string{"X-CSRF-Token": h.csrf}}
	s := &supportHarness{k, bot}
	texts := func() []string {
		deadline := time.Now().Add(3 * time.Second)
		for {
			mu.Lock()
			out := append([]string(nil), sent...)
			mu.Unlock()
			if len(out) >= 2 || time.Now().After(deadline) {
				return out
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	g, code, out := s.grant(map[string]any{"hours": 2, "level": "change", "note": "ticket 9"})
	if code != http.StatusCreated {
		t.Fatalf("grant: %d %s", code, out)
	}
	// The token's first use and the browser's entry are one "came in": one message.
	op := s.newOperator()
	op.do(http.MethodGet, "/users", nil, bearerOf(g.Token))
	op.do(http.MethodGet, "/users", nil, bearerOf(g.Token))
	if resp, body := s.newOperator().enter(g.Token); resp.StatusCode != http.StatusOK {
		t.Fatalf("enter: %d %s", resp.StatusCode, body)
	}
	if resp, _ := s.do(http.MethodDelete, s.api+"/support/grants/"+idOf(g.Grant.ID), nil, s.csrf); resp.StatusCode != http.StatusNoContent {
		t.Fatal("revoke")
	}
	got := texts()
	if len(got) != 2 {
		t.Fatalf("messages: %q", got)
	}
	if !strings.HasPrefix(got[0], "777|") || !strings.Contains(got[0], "Поддержка вошла") || !strings.Contains(got[0], "ticket 9") || strings.Contains(got[0], g.Token) {
		t.Errorf("the entry notice: %q", got[0])
	}
	if !strings.Contains(got[1], "закрыт") || !strings.Contains(got[1], "отозван") || strings.Contains(got[1], "—") {
		t.Errorf("the end notice: %q", got[1])
	}
	// A grant nobody used ends silently: the admin has nothing to be told.
	if _, code, _ := s.grant(map[string]any{"hours": 2, "level": "view"}); code != http.StatusCreated {
		t.Fatal("second grant")
	}
	h.advance(3 * time.Hour)
	if err := h.p.Support.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	n := len(sent)
	mu.Unlock()
	if n != 2 {
		t.Errorf("an unused grant told the admin: %d messages", n)
	}
}

// Support access works with the database as the single truth: a restarted panel still knows
// the grant, and a grant row without its sessions (cascade) leaves nothing behind.
func TestSupportGrantRemovalDropsSessions(t *testing.T) {
	s := newSupportHarness(t)
	g, _, _ := s.grant(map[string]any{"hours": 2, "level": "view"})
	op := s.newOperator()
	if resp, _ := op.enter(g.Token); resp.StatusCode != http.StatusOK {
		t.Fatal("enter")
	}
	if _, err := s.st.DB.Exec("DELETE FROM support_grants WHERE id = $1", g.Grant.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.st.DB.QueryRow("SELECT count(*) FROM sessions WHERE support_grant_id = $1", sql.NullInt64{Int64: g.Grant.ID, Valid: true}).Scan(&n); err != nil || n != 0 {
		t.Fatalf("sessions left: %d %v", n, err)
	}
	if resp, _ := op.do(http.MethodGet, "/users", nil, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("session of a deleted grant: %d", resp.StatusCode)
	}
}

// The host's operations under a support grant: a doctor run only reads, so the view level may
// call it; a restart writes, so it needs the change level (browser and token alike), and at
// view it leaves no request for the host.
func TestSupportOperatorHostOperations(t *testing.T) {
	dir := t.TempDir()
	logs := logbuf.New(100)
	s := newSupportHarness(t, func(o *Options) {
		o.DataDir, o.Logs = dir, logs
		o.Log = slog.New(logs.Handler(o.Log.Handler()))
	})
	restartRequest := filepath.Join(dir, "restart", "request")
	// Lines that name a subscriber: the operator is not to read the Telegram id or the address
	// (people hidden is the default of a grant), the admin is.
	wlog := slog.New(logs.Handler(nil))
	wlog.Info("billing: free plan", "tg", 123456789, "user", 5)
	wlog.Warn("billing: adapter refused a webhook", "ip", "1.2.3.4")
	// The host's unit: answers every doctor request until the test ends.
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
		req := filepath.Join(dir, "doctor", "request")
		for {
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Millisecond):
			}
			raw, err := os.ReadFile(req)
			if err != nil {
				continue
			}
			_ = os.Remove(req)
			var r struct{ ID string }
			_ = json.Unmarshal(raw, &r)
			out, _ := json.Marshal(nodeapi.HostReport{ID: r.ID, Kind: "panel", Items: []nodeapi.HostItem{{Level: "ok", Label: "Docker"}}})
			_ = os.WriteFile(filepath.Join(dir, "doctor", "report.json"), out, 0o644)
		}
	}()
	for _, level := range []string{"view", "change"} {
		g, code, out := s.grant(map[string]any{"hours": 6, "level": level})
		if code != http.StatusCreated {
			t.Fatalf("%s grant: %d %s", level, code, out)
		}
		op := s.newOperator()
		if resp, body := op.enter(g.Token); resp.StatusCode != http.StatusOK {
			t.Fatalf("enter: %d %s", resp.StatusCode, body)
		}
		for how, call := range map[string]func(method, path string, body any) (*http.Response, []byte){
			"browser": func(m, p string, b any) (*http.Response, []byte) { return op.write(m, p, b) },
			"token": func(m, p string, b any) (*http.Response, []byte) {
				return s.newOperator().do(m, p, b, bearerOf(g.Token))
			},
		} {
			resp, body := call(http.MethodGet, "/logs", nil)
			if resp.StatusCode != http.StatusOK {
				t.Errorf("%s %s logs: %d %s", level, how, resp.StatusCode, body)
			}
			if !strings.Contains(string(body), "free plan") || strings.Contains(string(body), "123456789") || strings.Contains(string(body), "1.2.3.4") {
				t.Errorf("%s %s logs: the lines must come without the subscriber's Telegram id and address: %s", level, how, body)
			}
			// A diagnostic is allowed at every level.
			for _, path := range []string{"/system/doctor", "/nodes/1/doctor"} {
				if resp, body := call(http.MethodPost, path, nil); resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"label":"Docker"`) {
					t.Errorf("%s %s POST %s: %d %s", level, how, path, resp.StatusCode, body)
				}
			}
			if level == "change" {
				// Allowed: the first request is taken (202), the next waits for the host (409).
				if resp, body := call(http.MethodPost, "/system/restart", nil); resp.StatusCode != http.StatusAccepted && detail(body) != "restart_pending" {
					t.Errorf("change %s restart: %d %s", how, resp.StatusCode, body)
				}
				continue
			}
			for _, path := range []string{"/system/restart", "/nodes/2/restart"} {
				resp, body := call(http.MethodPost, path, nil)
				if resp.StatusCode != http.StatusForbidden || detail(body) != "support_read_only" {
					t.Errorf("view %s POST %s: %d %s, want 403 support_read_only", how, path, resp.StatusCode, body)
				}
			}
			if _, err := os.Stat(restartRequest); err == nil {
				t.Fatalf("view %s left a restart request for the host", how)
			}
		}
	}
	// The admin's own answer keeps them.
	if _, body := s.do(http.MethodGet, s.api+"/logs", nil, nil); !strings.Contains(string(body), "123456789") || !strings.Contains(string(body), "1.2.3.4") {
		t.Errorf("the admin's logs lost the lines: %s", body)
	}
}

// The admin's switch: off, the operator is out at once and no access can be opened; the
// operator can never flip it; MIKAN_SUPPORT_ACCESS=off keeps it off whatever the setting.
func TestSupportAccessSwitch(t *testing.T) {
	s := newSupportHarness(t)
	g, code, out := s.grant(map[string]any{"hours": 2, "level": "change"})
	if code != http.StatusCreated {
		t.Fatalf("grant: %d %s", code, out)
	}
	op := s.newOperator()
	if resp, body := op.enter(g.Token); resp.StatusCode != http.StatusOK {
		t.Fatalf("enter: %d %s", resp.StatusCode, body)
	}
	// The operator cannot turn the switch, either way.
	if resp, body := op.write(http.MethodPatch, "/settings", map[string]any{"support_access": false}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("operator flips the switch: %d %s", resp.StatusCode, body)
	}
	if resp, body := s.do(http.MethodPatch, s.api+"/settings", map[string]any{"support_access": false}, s.csrf); resp.StatusCode != http.StatusOK {
		t.Fatalf("admin turns it off: %d %s", resp.StatusCode, body)
	}
	if resp, _ := op.do(http.MethodGet, "/support/whoami", nil, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("operator still in after off: %d", resp.StatusCode)
	}
	if resp, _ := op.do(http.MethodGet, "/support/whoami", nil, bearerOf(g.Token)); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("token still works after off: %d", resp.StatusCode)
	}
	if _, code, out := s.grant(map[string]any{"hours": 2, "level": "view"}); code != http.StatusForbidden || detail(out) != "support_disabled" {
		t.Fatalf("grant while off: %d %s", code, out)
	}
	resp, body := s.do(http.MethodGet, s.api+"/support/grants", nil, nil)
	var list struct {
		Off   string `json:"off"`
		Grant *struct {
			Status string `json:"status"`
		} `json:"grant"`
	}
	if err := json.Unmarshal(body, &list); err != nil || resp.StatusCode != http.StatusOK || list.Off != "setting" || list.Grant == nil || list.Grant.Status != "revoked" {
		t.Fatalf("list while off: %d %s", resp.StatusCode, body)
	}
	if resp, body := s.do(http.MethodPatch, s.api+"/settings", map[string]any{"support_access": true}, s.csrf); resp.StatusCode != http.StatusOK {
		t.Fatalf("admin turns it on: %d %s", resp.StatusCode, body)
	}
	if _, code, out := s.grant(map[string]any{"hours": 2, "level": "view"}); code != http.StatusCreated {
		t.Fatalf("grant after on: %d %s", code, out)
	}

	env := newSupportHarness(t, func(o *Options) { o.SupportOff = true })
	if _, code, out := env.grant(map[string]any{"hours": 2, "level": "view"}); code != http.StatusForbidden || detail(out) != "support_disabled" {
		t.Fatalf("grant with MIKAN_SUPPORT_ACCESS=off: %d %s", code, out)
	}
	_, body = env.do(http.MethodGet, env.api+"/support/grants", nil, nil)
	if err := json.Unmarshal(body, &list); err != nil || list.Off != "env" {
		t.Fatalf("list with the env switch: %s", body)
	}
}

// The operator ends their own access when the work is done, at any level, in the browser or
// by token; the admin has nothing to leave.
func TestSupportOperatorLeaves(t *testing.T) {
	s := newSupportHarness(t)
	if resp, body := s.do(http.MethodPost, s.api+"/support/leave", nil, s.csrf); resp.StatusCode != http.StatusForbidden || detail(body) != "support_only" {
		t.Fatalf("admin leaves: %d %s", resp.StatusCode, body)
	}
	g, code, out := s.grant(map[string]any{"hours": 2, "level": "view"})
	if code != http.StatusCreated {
		t.Fatalf("grant: %d %s", code, out)
	}
	op := s.newOperator()
	if resp, body := op.enter(g.Token); resp.StatusCode != http.StatusOK {
		t.Fatalf("enter: %d %s", resp.StatusCode, body)
	}
	if resp, _ := op.do(http.MethodPost, "/support/leave", nil, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("leave without CSRF: %d", resp.StatusCode)
	}
	if resp, body := op.write(http.MethodPost, "/support/leave", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("leave at view: %d %s", resp.StatusCode, body)
	}
	if resp, _ := op.do(http.MethodGet, "/support/whoami", nil, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("still in after leaving: %d", resp.StatusCode)
	}
	if resp, _ := op.do(http.MethodGet, "/support/whoami", nil, bearerOf(g.Token)); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("token after leaving: %d", resp.StatusCode)
	}
	_, body := s.do(http.MethodGet, s.api+"/support/grants", nil, nil)
	var list struct {
		Grant struct {
			Status string `json:"status"`
		} `json:"grant"`
	}
	if err := json.Unmarshal(body, &list); err != nil || list.Grant.Status != "revoked" {
		t.Fatalf("grant after leaving: %s", body)
	}
	// By token: a second access, left over the API.
	g2, code, out := s.grant(map[string]any{"hours": 2, "level": "change"})
	if code != http.StatusCreated {
		t.Fatalf("grant 2: %d %s", code, out)
	}
	if resp, body := op.do(http.MethodPost, "/support/leave", nil, bearerOf(g2.Token)); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("leave by token: %d %s", resp.StatusCode, body)
	}
	if resp, _ := op.do(http.MethodGet, "/support/whoami", nil, bearerOf(g2.Token)); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("token 2 after leaving: %d", resp.StatusCode)
	}
}

// Access opens only for a code the support bot says yes to, and its link goes to the bot
// alone: a refused code opens nothing, a link the bot did not take is withdrawn.
func TestSupportAccessNeedsTheBotsCode(t *testing.T) {
	s := newSupportHarness(t)
	count := func() int {
		var n int
		if err := s.st.DB.QueryRow("SELECT COUNT(*) FROM support_grants").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for name, c := range map[string]struct {
		code string
		want int
		msg  string
	}{
		"no code":    {"", http.StatusUnprocessableEntity, ""},
		"short":      {"ABC", http.StatusUnprocessableEntity, ""},
		"not a code": {"ABCD 23!5", http.StatusUnprocessableEntity, "support_code"},
		"cyrillic":   {"АБВГ2345", http.StatusUnprocessableEntity, ""},
		"bad by bot": {"ZZZZ9999", http.StatusUnprocessableEntity, "support_code_invalid"},
	} {
		if name == "bad by bot" {
			s.bot.claimErr = support.ErrCode
		}
		_, code, out := s.grant(map[string]any{"hours": 2, "level": "view", "code": c.code})
		if code != c.want || !strings.Contains(string(out), c.msg) {
			t.Errorf("%s: %d %s", name, code, out)
		}
		s.bot.claimErr = nil
	}
	if n := count(); n != 0 {
		t.Fatalf("%d grants opened without a good code", n)
	}

	// The bot is down: nothing opens.
	s.bot.claimErr = fmt.Errorf("%w: dial", support.ErrBotDown)
	if _, code, out := s.grant(map[string]any{"hours": 2, "level": "view"}); code != http.StatusBadGateway || !strings.Contains(string(out), "support_bot_down") {
		t.Errorf("bot down: %d %s", code, out)
	}
	s.bot.claimErr = nil
	if n := count(); n != 0 {
		t.Fatalf("%d grants opened with the bot down", n)
	}

	// A dashed, lower-case code is the same code.
	if _, code, out := s.grant(map[string]any{"hours": 2, "level": "view", "code": "abcd-2345"}); code != http.StatusCreated {
		t.Fatalf("dashed code: %d %s", code, out)
	}
	if last := s.bot.claims[len(s.bot.claims)-1]; last != "ABCD2345" {
		t.Errorf("the bot got %q", last)
	}

	// The bot refuses the link: the grant is withdrawn at once and its token opens nothing.
	s.bot.attachErr = &support.LinkError{Reason: "panel is not an official build"}
	before := len(s.bot.links)
	_, code, out := s.grant(map[string]any{"hours": 2, "level": "change"})
	if code != http.StatusUnprocessableEntity || !strings.Contains(string(out), "support_link_refused") || !strings.Contains(string(out), "official build") {
		t.Fatalf("refused link: %d %s", code, out)
	}
	s.bot.attachErr = nil
	if len(s.bot.links) != before {
		t.Fatal("a refused link was kept")
	}
	var live int
	if err := s.st.DB.QueryRow("SELECT COUNT(*) FROM support_grants WHERE revoked_at IS NULL").Scan(&live); err != nil || live != 0 {
		t.Fatalf("live grants after a refused link: %d %v", live, err)
	}
	_, body := s.do(http.MethodGet, s.api+"/support/grants", nil, nil)
	if !strings.Contains(string(body), "undelivered") {
		t.Errorf("the journal does not say why: %s", body)
	}
}

// Without a support bot no access can be opened.
func TestSupportAccessWithoutABot(t *testing.T) {
	s := newSupportHarness(t, func(o *Options) { o.SupportBot = nil })
	if _, code, out := s.grant(map[string]any{"hours": 2, "level": "view"}); code != http.StatusServiceUnavailable || !strings.Contains(string(out), "support_bot_down") {
		t.Errorf("no bot: %d %s", code, out)
	}
}

// An operator gets a protocol's server key as hidden, and a template it saves or checks
// with the key still hidden keeps the saved key.
func TestSupportOperatorEditsAHiddenTemplate(t *testing.T) {
	s := newSupportHarness(t)
	g, code, out := s.grant(map[string]any{"hours": 2, "level": "change"})
	if code != http.StatusCreated {
		t.Fatalf("grant: %d %s", code, out)
	}
	op := s.newOperator()
	if resp, body := op.enter(g.Token); resp.StatusCode != http.StatusOK {
		t.Fatalf("enter: %d %s", resp.StatusCode, body)
	}
	var id int64
	var saved string
	if err := s.st.DB.QueryRow("SELECT id, config FROM inbounds WHERE config LIKE '%private-key%' ORDER BY id LIMIT 1").Scan(&id, &saved); err != nil {
		t.Fatal(err)
	}
	resp, body := op.do(http.MethodGet, "/inbounds", nil, nil)
	var list []struct {
		ID     int64  `json:"id"`
		NodeID int64  `json:"node_id"`
		Config string `json:"config"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &list) != nil {
		t.Fatalf("inbounds: %d %s", resp.StatusCode, body)
	}
	shown := ""
	var node int64
	for _, in := range list {
		if in.ID == id {
			shown, node = in.Config, in.NodeID
		}
	}
	if !strings.Contains(shown, "<hidden>") || shown == saved {
		t.Fatalf("the operator's template: %q", shown)
	}
	if resp, body := op.write(http.MethodPost, "/inbounds/validate", map[string]any{"config": shown, "node_id": node, "inbound_id": id}); resp.StatusCode != http.StatusOK {
		t.Fatalf("check with the key hidden: %d %s", resp.StatusCode, body)
	}
	if resp, body := op.write(http.MethodPatch, "/inbounds/"+idOf(id), map[string]any{"config": shown}); resp.StatusCode != http.StatusOK {
		t.Fatalf("save with the key hidden: %d %s", resp.StatusCode, body)
	}
	var now string
	if err := s.st.DB.QueryRow("SELECT config FROM inbounds WHERE id = $1", id).Scan(&now); err != nil {
		t.Fatal(err)
	}
	key := func(src string) string {
		t.Helper()
		tpl, err := proto.Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		k, _ := tpl["reality-config"].(map[string]any)["private-key"].(string)
		return k
	}
	if key(now) == "" || key(now) != key(saved) {
		t.Fatalf("the saved key: %q, was %q", key(now), key(saved))
	}
}
