package server

import (
	"io"
	"mikan/internal/frontpage"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mikan/internal/panel/settings"
)

// A site of the admin's own answers every path the panel does not own (GitHub issue #67);
// the secret paths stay the panel's, hidden files stay hidden, and without the site the
// panel answers as before.
func TestOwnSite(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{"index.html": "<h1>Bakery</h1>", "robots.txt": "User-agent: *\n", "404.html": "<p>no such page</p>",
		".env": "SECRET=1", "menu/index.html": "<p>menu</p>"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	panel := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "panel") })
	s := New(panel, panel)
	s.SetPaths(settings.Paths{Admin: "adm1n", Sub: "s0b"})
	get := func(p string) (int, string, http.Header) {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		return rec.Code, rec.Body.String(), rec.Header()
	}

	if code, body, _ := get("/"); code != http.StatusNotFound || body != "404 Not Found\n" {
		t.Fatalf("no site set: %d %q", code, body)
	}
	s.SetSite(OwnSite(dir, func() string { return "cats" }))
	for p, want := range map[string]struct {
		code int
		body string
	}{
		"/":           {200, "<h1>Bakery</h1>"},
		"/robots.txt": {200, "User-agent: *\n"},
		"/menu/":      {200, "<p>menu</p>"},
		"/nope":       {404, "<p>no such page</p>"},
		"/.env":       {404, "<p>no such page</p>"},
		"/adm1n/":     {200, "panel"},
		"/s0b/":       {200, "panel"},
	} {
		code, body, h := get(p)
		if code != want.code || body != want.body {
			t.Errorf("%s: %d %q", p, code, body)
		}
		if p != "/adm1n/" && p != "/s0b/" && h.Get("Content-Security-Policy") != "" {
			t.Errorf("%s: the panel's policy headers set the site apart", p)
		}
	}

	s.SetSite(OwnSite(filepath.Join(dir, "missing"), nil))
	if code, body, _ := get("/robots.txt"); code != http.StatusNotFound || body != "404 Not Found\n" {
		t.Fatalf("no site on disk: %d %q", code, body)
	}

	// Without a site of the admin's, "/" is the chosen template; the rest stays 404.
	template := "maintenance"
	s.SetSite(OwnSite(filepath.Join(dir, "missing"), func() string { return template }))
	if code, body, h := get("/"); code != http.StatusOK || !strings.Contains(body, "Scheduled maintenance") || h.Get("Content-Security-Policy") != "" {
		t.Fatalf("maintenance: %d %.80q", code, body)
	}
	if code, _, _ := get("/robots.txt"); code != http.StatusNotFound {
		t.Fatalf("a template has one page: %d", code)
	}
	template = "cats"
	if _, body, _ := get("/"); !strings.Contains(body, "Cat Clicker") {
		t.Fatalf("cats: %.80q", body)
	}
	template = ""
	if code, _, _ := get("/"); code != http.StatusNotFound {
		t.Fatalf("no template: %d", code)
	}
	empty := t.TempDir()
	template = "cats"
	s.SetSite(OwnSite(empty, func() string { return template }))
	if _, body, _ := get("/"); !strings.Contains(body, "Cat Clicker") {
		t.Fatalf("an empty www: %.80q", body)
	}
	if HasOwnSite(empty) || !HasOwnSite(dir) {
		t.Fatal("HasOwnSite")
	}
}

// The admin's own page at the panel's address runs no script and loads nothing from another
// address: the admin's session lives at this address too. The built-in pages keep their
// scripts. A site of several files answers each address with its file.
func TestOwnPageIsSandboxed(t *testing.T) {
	panel := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "panel") })
	s := New(panel, panel)
	s.SetPaths(settings.Paths{Admin: "adm1n", Sub: "s0b"})
	kind := "custom"
	s.SetSite(OwnSite(t.TempDir(), func() string { return kind }))
	get := func() (int, string, string) {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		return rec.Code, rec.Body.String(), rec.Header().Get("Content-Security-Policy")
	}
	t.Cleanup(func() { frontpage.SetCustom(nil) })
	if code, _, _ := get(); code != http.StatusNotFound {
		t.Fatalf("no page saved: %d", code)
	}
	frontpage.SetCustom([]byte("<!doctype html><title>Bakery</title>"))
	code, body, csp := get()
	if code != http.StatusOK || body != "<!doctype html><title>Bakery</title>" || csp != frontpage.SitePolicy || !strings.HasPrefix(csp, "default-src 'none'") {
		t.Fatalf("the admin's page: %d %q, policy %q", code, body, csp)
	}
	// With the site's files every address has its own; one it has not gets the site's own 404.
	t.Cleanup(func() { frontpage.SetSite(nil) })
	frontpage.SetSite([]frontpage.SiteFile{
		{Path: "index.html", Type: "text/html", Data: []byte("<!doctype html><title>Home</title>")},
		{Path: "about.html", Type: "text/html", Data: []byte("<!doctype html><title>About</title>")},
		{Path: "404.html", Type: "text/html", Data: []byte("<!doctype html><title>No such page</title>")},
		{Path: "fonts/inter-latin.woff2", Type: "font/woff2", Data: []byte("wOF2")},
	})
	at := func(p, method string) (int, string, http.Header) {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(method, p, nil))
		return rec.Code, rec.Body.String(), rec.Header()
	}
	for p, want := range map[string]struct {
		code int
		body string
		kind string
	}{
		"/":                        {200, "<!doctype html><title>Home</title>", "text/html; charset=utf-8"},
		"/about":                   {200, "<!doctype html><title>About</title>", "text/html; charset=utf-8"},
		"/about/":                  {200, "<!doctype html><title>About</title>", "text/html; charset=utf-8"},
		"/fonts/inter-latin.woff2": {200, "wOF2", "font/woff2"},
		"/about.html":              {404, "<!doctype html><title>No such page</title>", "text/html; charset=utf-8"},
		"/404":                     {404, "<!doctype html><title>No such page</title>", "text/html; charset=utf-8"},
		"/nope":                    {404, "<!doctype html><title>No such page</title>", "text/html; charset=utf-8"},
		"/fonts/../index.html":     {404, "<!doctype html><title>No such page</title>", "text/html; charset=utf-8"},
		"/.env":                    {404, "<!doctype html><title>No such page</title>", "text/html; charset=utf-8"},
	} {
		code, body, h := at(p, http.MethodGet)
		if code != want.code || body != want.body || h.Get("Content-Type") != want.kind {
			t.Errorf("%s: %d %q %s", p, code, body, h.Get("Content-Type"))
		}
		if page := strings.HasPrefix(want.kind, "text/html"); page != (h.Get("Content-Security-Policy") == frontpage.SitePolicy) {
			t.Errorf("%s: policy %q", p, h.Get("Content-Security-Policy"))
		}
	}
	if code, body, _ := at("/about", http.MethodPost); code != http.StatusNotFound || strings.Contains(body, "About") {
		t.Fatalf("a post to a page: %d %q", code, body)
	}
	if code, body, _ := at("/about", http.MethodHead); code != http.StatusOK || body != "" {
		t.Fatalf("a head of a page: %d %q", code, body)
	}
	kind = "cats"
	if code, _, csp := get(); code != http.StatusOK || csp == frontpage.SitePolicy {
		t.Fatalf("a built-in page: %d, policy %q", code, csp)
	}
}
