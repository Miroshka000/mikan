package subrelay

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const tokenPath = "/abcdefghijklmnopqrstuvwx"

func TestAllowed(t *testing.T) {
	for _, p := range []string{tokenPath, tokenPath + "/info", tokenPath + "/docs/12", "/assets/app.js", "/tg"} {
		if !Allowed("GET", p) {
			t.Errorf("rejected %s", p)
		}
	}
	for _, p := range []string{"/admin/api/v1/users", "/assets/../admin", "//assets/a", "/assets/%2e%2e/a", "/assets/\\admin", "/pay/webhook", tokenPath + "/devices/1/name"} {
		if Allowed("GET", p) {
			t.Errorf("accepted %s", p)
		}
	}
	if !Allowed("POST", tokenPath+"/devices/1/name") || Allowed("DELETE", tokenPath) {
		t.Fatal("method authorization")
	}
}

func TestProxy(t *testing.T) {
	var profiles, assets atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer edge-key" {
			t.Error("edge credential missing")
		}
		if r.Header.Get("Cookie") != "" || r.Header.Get("X-Forwarded-For") != "" {
			t.Error("forwarded client secrets or untrusted IP")
		}
		if r.Header.Get("X-Mikan-Client-IP") != "192.0.2.1" {
			t.Error("wrong client IP")
		}
		if strings.Contains(r.URL.Path, "assets") {
			assets.Add(1)
		} else {
			profiles.Add(1)
		}
		w.Header().Set("Subscription-Userinfo", "upload=1; download=2")
		w.Header().Set("Set-Cookie", "admin=secret")
		if r.URL.Query().Get("redirect") == "1" {
			http.Redirect(w, r, "https://example.com", 302)
			return
		}
		_, _ = io.WriteString(w, "profile")
	}))
	defer upstream.Close()
	p, err := New(upstream.URL+"/api/v1/subscription-relay", "edge-key", "sub")
	p.SetCircuitBreaker(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	p.client = upstream.Client()
	p.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	get := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "https://sub.example/sub"+path, nil)
		r.Header.Set("Authorization", "Bearer attacker")
		r.Header.Set("Cookie", "admin=client")
		r.Header.Set("X-Mikan-Client-IP", "spoof")
		r.Header.Set("X-Forwarded-For", "spoof")
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)
		return w
	}
	for range 2 {
		w := get(tokenPath)
		if w.Code != 200 || w.Body.String() != "profile" || w.Header().Get("Cache-Control") != "private, no-store" || w.Header().Get("Set-Cookie") != "" || w.Header().Get("Subscription-Userinfo") == "" {
			t.Fatalf("profile response: %v", w)
		}
		if w := get("/assets/a.js"); w.Code != 200 {
			t.Fatal(w.Code)
		}
	}
	if profiles.Load() != 2 || assets.Load() != 1 {
		t.Fatalf("wrong cache behavior: profiles=%d assets=%d", profiles.Load(), assets.Load())
	}
	if w := get("/admin/api"); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if w := get(tokenPath + "?redirect=1"); w.Code != 502 {
		t.Fatal(w.Code)
	}
}

func TestConfig(t *testing.T) {
	for _, u := range []string{"http://panel/relay", "https://user:pass@panel/relay", "https://panel/relay?q=1", "https://panel/relay#fragment", "https:///relay"} {
		if _, err := New(u, "key", "sub"); err == nil {
			t.Errorf("accepted %s", u)
		}
	}
}
