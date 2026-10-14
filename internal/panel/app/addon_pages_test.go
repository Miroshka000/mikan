package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mikan/internal/panel/addons"
	"mikan/internal/panel/store/db"
)

// fakePages is an addon of protocol 2 with pages: it remembers what reached it.
type fakePages struct {
	mu    sync.Mutex
	files []*http.Request
	calls []map[string]any
}

func (f *fakePages) serve(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/v2/info":
			_, _ = io.WriteString(w, `{"id":"pager","protocol":2,"version":"1.0.0","kind":"ui","name":{"en":"Pager"},"ui":{"entry":"/ui/index.html","title":{"en":"Texts","ru":"Тексты"}}}`)
		case r.URL.Path == "/v2/ui/rpc" && r.Method == http.MethodPost:
			var call map[string]any
			_ = json.NewDecoder(r.Body).Decode(&call)
			f.mu.Lock()
			f.calls = append(f.calls, call)
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if call["path"] == "/fail" {
				w.WriteHeader(http.StatusConflict)
				_, _ = io.WriteString(w, `{"code":"taken","message":"no"}`)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"echo": call["body"], "path": call["path"]})
		case strings.HasPrefix(r.URL.Path, "/ui/"):
			f.mu.Lock()
			f.files = append(f.files, r.Clone(context.Background()))
			f.mu.Unlock()
			h := w.Header()
			h.Set("Content-Type", "text/html; charset=utf-8")
			h.Set("Set-Cookie", "stolen=1; Path=/")
			h.Set("Content-Security-Policy", "default-src *")
			h.Set("Access-Control-Allow-Origin", "*")
			h.Set("X-Secret", "1")
			h.Set("ETag", `"v1"`)
			_, _ = io.WriteString(w, "<!doctype html><script src=app.js></script>")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// An addon's pages: a signed address for the admin's session, files proxied with the frame's
// headers and nothing of the admin's credentials, calls forwarded with the session and written
// to the audit log. Only an addon of protocol 2 whose pages the admin approved has them.
func TestAddonPages(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, func(o *Options) { o.DataDir = dir })
	fake := &fakePages{}
	srv := fake.serve(t)
	listen := strings.TrimPrefix(srv.URL, "http://")
	st := addons.State{HostVersion: "0.5.1.0", Adapters: map[string]addons.Installed{
		"pager":   {Version: "1.0.0", Listen: listen, Token: "tok", Protocol: 2, Kind: addons.KindUI, Status: "running", PanelToken: "p1"},
		"nogrant": {Version: "1.0.0", Listen: listen, Token: "tok", Protocol: 2, Kind: addons.KindUI, Status: "running", PanelToken: "p2"},
		"pay":     {Version: "1.0.0", Listen: listen, Token: "tok", Status: "running"},
		"client":  {Version: "1.0.0", Token: "tok", Protocol: 2, Kind: addons.KindBot, Mode: addons.ModeClient, Status: "running", PanelToken: "p3"},
	}}
	data, _ := json.Marshal(st)
	if err := os.MkdirAll(filepath.Join(dir, "addons"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "addons", "state.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for id, perms := range map[string]string{"pager": `["kv","ui"]`, "nogrant": `["kv"]`, "client": `["ui"]`, "pay": `["ui"]`} {
		if err := h.st.Q.UpsertAddonGrant(ctx, db.UpsertAddonGrantParams{AddonID: id, Version: "1.0.0", Permissions: perms, ApprovedAt: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	api := "/" + adminPath + "/api/v1/addons/"

	// Who has no pages.
	for id, want := range map[string]string{"pay": "addon_no_pages", "client": "addon_no_pages", "gone": "addon_no_pages", "nogrant": "addon_ui_forbidden"} {
		status := http.StatusNotFound
		if want == "addon_ui_forbidden" {
			status = http.StatusForbidden
		}
		if resp, body := h.do(http.MethodGet, api+id+"/ui", nil, nil); resp.StatusCode != status || !strings.Contains(string(body), want) {
			t.Errorf("%s: page %d %s", id, resp.StatusCode, body)
		}
		if resp, body := h.do(http.MethodPost, api+id+"/ui/rpc", map[string]any{"method": "GET", "path": "/x"}, csrf); resp.StatusCode != status || !strings.Contains(string(body), want) {
			t.Errorf("%s: call %d %s", id, resp.StatusCode, body)
		}
	}

	resp, body := h.do(http.MethodGet, api+"pager/ui", nil, nil)
	var page struct {
		URL       string            `json:"url"`
		Title     map[string]string `json:"title"`
		ExpiresAt int64             `json:"expires_at"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &page) != nil {
		t.Fatalf("page: %d %s", resp.StatusCode, body)
	}
	if !strings.HasPrefix(page.URL, "addons-ui/pager/") || !strings.HasSuffix(page.URL, "/index.html") || page.Title["ru"] != "Тексты" ||
		page.ExpiresAt != h.now.Add(10*time.Minute).Unix() {
		t.Fatalf("page: %+v", page)
	}
	file := "/" + adminPath + "/" + page.URL

	// The frame's document comes with the admin's cookie (the jar sends it); the addon gets
	// neither it nor the browser's Authorization, only its own token.
	resp, body = h.do(http.MethodGet, file+"?v=2", nil, map[string]string{"Authorization": "Bearer mk_nope", "Accept-Language": "ru"})
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "app.js") {
		t.Fatalf("file: %d %s", resp.StatusCode, body)
	}
	hd := resp.Header
	for k, want := range map[string]string{
		"Content-Security-Policy":      "sandbox allow-scripts; default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'self'; base-uri 'none'; form-action 'none'; object-src 'none'",
		"X-Frame-Options":              "SAMEORIGIN",
		"Cross-Origin-Resource-Policy": "cross-origin",
		"X-Content-Type-Options":       "nosniff",
		"Referrer-Policy":              "no-referrer",
		"Content-Type":                 "text/html; charset=utf-8",
		"Etag":                         `"v1"`,
		"Cache-Control":                "no-store",
		"Set-Cookie":                   "",
		"Access-Control-Allow-Origin":  "",
		"X-Secret":                     "",
	} {
		if got := strings.Join(hd.Values(k), " | "); got != want {
			t.Errorf("%s: %q, want %q", k, got, want)
		}
	}
	fake.mu.Lock()
	got := fake.files[len(fake.files)-1]
	fake.mu.Unlock()
	if got.Method != http.MethodGet || got.URL.Path != "/ui/index.html" || got.URL.RawQuery != "v=2" || got.Header.Get("Cookie") != "" ||
		got.Header.Get("Authorization") != "Bearer tok" || got.Header.Get("Accept-Language") != "ru" {
		t.Fatalf("the addon got %s %s cookie=%q auth=%q lang=%q", got.Method, got.URL, got.Header.Get("Cookie"), got.Header.Get("Authorization"), got.Header.Get("Accept-Language"))
	}

	// A file the page loads from inside the frame comes without the cookie: the address is enough.
	bare := &http.Client{Transport: h.ts.Client().Transport}
	r2, err := bare.Get(h.ts.URL + strings.TrimSuffix(file, "index.html") + "app.js")
	if err != nil {
		t.Fatal(err)
	}
	_ = r2.Body.Close()
	if r2.StatusCode != http.StatusOK {
		t.Fatalf("a file without the cookie: %d", r2.StatusCode)
	}
	// Only GET.
	if resp, _ := h.do(http.MethodPost, file, nil, csrf); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST to a file: %d", resp.StatusCode)
	}
	// A changed address, another addon's or a missing slash: the bare 404.
	sig := strings.Split(page.URL, "/")[2]
	other := "A"
	if sig[10] == 'A' {
		other = "B"
	}
	for _, p := range []string{
		strings.Replace(file, sig, sig[:10]+other+sig[11:], 1),
		strings.Replace(file, "/pager/", "/nogrant/", 1),
		"/" + adminPath + "/addons-ui/pager/" + sig,
	} {
		if resp, body := h.do(http.MethodGet, p, nil, nil); resp.StatusCode != http.StatusNotFound || string(body) != "404 Not Found\n" {
			t.Errorf("%s: %d %q", p, resp.StatusCode, body)
		}
	}

	// Calls: forwarded with the admin's language, the addon's status and JSON as they are; a
	// change is audited without its body.
	call := func(method, path string, body any, hdr map[string]string) (int, map[string]any) {
		t.Helper()
		resp, out := h.do(http.MethodPost, api+"pager/ui/rpc", map[string]any{"method": method, "path": path, "body": body, "lang": "en"}, hdr)
		var res map[string]any
		_ = json.Unmarshal(out, &res)
		return resp.StatusCode, res
	}
	if status, _ := call("POST", "/save", map[string]any{"a": 1}, nil); status != http.StatusForbidden {
		t.Fatalf("a call without CSRF: %d", status)
	}
	status, res := call("POST", "/save", map[string]any{"secret": "s3cr3t"}, csrf)
	if status != http.StatusOK || res["status"] != float64(201) || res["body"].(map[string]any)["path"] != "/save" {
		t.Fatalf("call: %d %v", status, res)
	}
	fake.mu.Lock()
	c := fake.calls[len(fake.calls)-1]
	fake.mu.Unlock()
	if c["method"] != "POST" || c["path"] != "/save" || c["admin"].(map[string]any)["lang"] != "en" || c["body"].(map[string]any)["secret"] != "s3cr3t" {
		t.Fatalf("the addon got %v", c)
	}
	if status, res := call("DELETE", "/fail", nil, csrf); status != http.StatusOK || res["status"] != float64(409) || res["body"].(map[string]any)["code"] != "taken" {
		t.Fatalf("an addon's refusal: %d %v", status, res)
	}
	if status, _ := call("GET", "/list", nil, csrf); status != http.StatusOK {
		t.Fatalf("GET call: %d", status)
	}
	if status, _ := call("PATCH", "/list", nil, csrf); status != http.StatusUnprocessableEntity {
		t.Fatalf("PATCH call: %d", status)
	}
	if status, _ := call("GET", "list", nil, csrf); status != http.StatusUnprocessableEntity {
		t.Fatalf("a path without the slash: %d", status)
	}
	rows, err := h.st.Q.ListAudit(ctx, db.ListAuditParams{BeforeID: 1 << 62, Lim: 100})
	if err != nil {
		t.Fatal(err)
	}
	var audited []string
	for _, r := range rows {
		if r.Action != "addon.ui_call" {
			continue
		}
		if r.TargetID != (sql.NullString{String: "pager", Valid: true}) || strings.Contains(r.Details.String, "s3cr3t") {
			t.Errorf("audit: %+v", r)
		}
		audited = append(audited, r.Details.String)
	}
	if len(audited) != 2 || !strings.Contains(strings.Join(audited, " "), `"path":"/save"`) || !strings.Contains(strings.Join(audited, " "), `"path":"/fail"`) {
		t.Fatalf("audited: %v", audited)
	}

	// The grant loses ui: the address and the calls stop at once.
	if err := h.st.Q.UpsertAddonGrant(ctx, db.UpsertAddonGrantParams{AddonID: "pager", Version: "1.0.0", Permissions: `["kv"]`, ApprovedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if resp, _ := h.do(http.MethodGet, file, nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a file after the grant lost ui: %d", resp.StatusCode)
	}
	if status, _ := call("GET", "/list", nil, csrf); status != http.StatusForbidden {
		t.Fatalf("a call after the grant lost ui: %d", status)
	}
	if err := h.st.Q.UpsertAddonGrant(ctx, db.UpsertAddonGrantParams{AddonID: "pager", Version: "1.0.0", Permissions: `["ui"]`, ApprovedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if resp, _ := h.do(http.MethodGet, file, nil, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("a file with the grant back: %d", resp.StatusCode)
	}

	// Ten minutes later the address is over; a logout ends it sooner.
	h.now = h.now.Add(10 * time.Minute)
	if resp, _ := h.do(http.MethodGet, file, nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("an expired address: %d", resp.StatusCode)
	}
	resp, body = h.do(http.MethodGet, api+"pager/ui", nil, nil)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &page) != nil {
		t.Fatalf("page again: %d %s", resp.StatusCode, body)
	}
	file = "/" + adminPath + "/" + page.URL
	if resp, _ := h.do(http.MethodPost, "/"+adminPath+"/api/v1/auth/logout", nil, csrf); resp.StatusCode/100 != 2 {
		t.Fatalf("logout: %d", resp.StatusCode)
	}
	if resp, _ := h.do(http.MethodGet, file, nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("an address after logout: %d", resp.StatusCode)
	}
}
