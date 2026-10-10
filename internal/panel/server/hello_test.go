package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mikan/internal/panel/settings"
)

// A hello the handler does not take gets the answer of any unknown path, byte for byte;
// one it takes is answered by it alone; the secret paths come first.
func TestHelloRoute(t *testing.T) {
	admin := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "admin") })
	s := New(admin, http.NotFoundHandler())
	s.SetPaths(settings.Paths{Admin: "secret-admin", Sub: "secret-sub"})
	var asked int
	s.SetHello(func(w http.ResponseWriter, r *http.Request) bool {
		asked++
		if r.Header.Get("X-Valid") != "1" {
			return false
		}
		_, _ = io.WriteString(w, "hello")
		return true
	})
	get := func(method, path string, valid bool) (int, string) {
		req := httptest.NewRequest(method, path, strings.NewReader("{}"))
		if valid {
			req.Header.Set("X-Valid", "1")
		}
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	code, body := get(http.MethodPost, "/.well-known/mikan-node-hello", false)
	otherCode, otherBody := get(http.MethodPost, "/anything", false)
	if code != http.StatusNotFound || code != otherCode || body != otherBody {
		t.Fatalf("a refused hello: %d %q, any path: %d %q", code, body, otherCode, otherBody)
	}
	if code, body := get(http.MethodPost, "/.well-known/mikan-node-hello", true); code != http.StatusOK || body != "hello" {
		t.Fatalf("a hello: %d %q", code, body)
	}
	before := asked
	if _, body := get(http.MethodGet, "/secret-admin/", true); body != "admin" || asked != before {
		t.Fatalf("the admin path: %q, hello asked %d times", body, asked-before)
	}
}
