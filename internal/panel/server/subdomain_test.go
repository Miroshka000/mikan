package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"mikan/internal/panel/settings"
)

// The subscription domain's host serves subscriptions alone: tokens stand at the root,
// the panel's paths are a 404 there, other hosts of the same listener keep the paths.
func TestSubDomainRouting(t *testing.T) {
	panel := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "panel") })
	sub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "sub:"+r.URL.Path) })
	s := New(panel, sub)
	s.SetPaths(settings.Paths{Admin: "adm1n", Sub: "s0b"})
	get := func(host, p string) (int, string) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, p, nil)
		req.Host = host
		s.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	// Without a subscription domain the paths decide, whoever is addressed.
	for _, c := range []struct{ host, p, want string }{
		{"panel.example.com", "/adm1n/", "panel"},
		{"panel.example.com", "/s0b/t0k3n", "sub:/t0k3n"},
		{"sub.example.com", "/s0b/t0k3n", "sub:/t0k3n"},
		{"sub.example.com", "/adm1n/", "panel"},
	} {
		code, got := get(c.host, c.p)
		if code != http.StatusOK || got != c.want {
			t.Errorf("%s%s: %d %q, want %q", c.host, c.p, code, got, c.want)
		}
	}

	s.SetSubDomain("Sub.Example.COM.")
	for _, c := range []struct{ host, p, want string }{
		// The subscription host: everything goes to the subscription handler, tokens at
		// the root included; the port in the Host is not one. The panel's paths — the
		// admin's and the secret subscription one — are not served to it.
		{"sub.example.com", "/t0k3n", "sub:/t0k3n"},
		{"sub.example.com:2053", "/t0k3n", "sub:/t0k3n"},
		{"SUB.example.com", "/tg", "sub:/tg"},
		{"[2001:db8::1]:1", "/x", "404 Not Found\n"},
		{"sub.example.com", "/s0b/t0k3n", "sub:/s0b/t0k3n"},
		{"sub.example.com", "/adm1n/", "sub:/adm1n/"},
		{"sub.example.com", "/", "sub:/"},
		// Other hosts are as they always were.
		{"panel.example.com", "/adm1n/", "panel"},
		{"panel.example.com", "/s0b/t0k3n", "sub:/t0k3n"},
		{"other.example.com", "/t0k3n", "404 Not Found\n"},
	} {
		code, got := get(c.host, c.p)
		if got != c.want || (c.want != "404 Not Found\n" && code != http.StatusOK) || (c.want == "404 Not Found\n" && code != http.StatusNotFound) {
			t.Errorf("%s%s: %d %q, want %q", c.host, c.p, code, got, c.want)
		}
	}

	s.SetSubDomain("")
	code, got := get("sub.example.com", "/t0k3n")
	if code != http.StatusNotFound || got != "404 Not Found\n" {
		t.Fatalf("cleared: %d %q", code, got)
	}
}
