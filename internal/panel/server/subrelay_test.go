package server

import (
	"mikan/internal/panel/settings"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRelayUsesSecretAndUnknownSiteFallback(t *testing.T) {
	s := New(http.NotFoundHandler(), http.NotFoundHandler())
	s.SetPaths(settings.Paths{Admin: "secret-admin", Sub: "secret-sub"})
	s.SetSite(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(418); w.Write([]byte("own site")) }))
	enabled := false
	calls := 0
	s.SetSubscriptionRelay(func(w http.ResponseWriter, r *http.Request) bool {
		calls++
		if !enabled || r.Header.Get("Authorization") != "Bearer dedicated" {
			return false
		}
		if r.URL.Path != "/abcdefghijklmnopqrstuvwx" {
			t.Errorf("wrong relay path: %s", r.URL.Path)
		}
		w.WriteHeader(204)
		return true
	})
	fetch := func(handler http.Handler, path, auth string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", auth)
		handler.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/api/subscription/abcdefghijklmnopqrstuvwx", "/unknown/api/subscription/abcdefghijklmnopqrstuvwx"} {
		if w := fetch(s, path, ""); w.Code != 418 {
			t.Fatal(w.Code)
		}
	}
	if calls != 0 {
		t.Fatal("public path probed relay")
	}
	for _, auth := range []string{"", "Bearer dedicated"} {
		if w := fetch(s, "/secret-admin/api/subscription/abcdefghijklmnopqrstuvwx", auth); w.Code != 418 || w.Body.String() != "own site" {
			t.Fatal("disabled endpoint fingerprinted")
		}
	}
	enabled = true
	if w := fetch(s, "/secret-admin/api/subscription/abcdefghijklmnopqrstuvwx", ""); w.Code != 418 {
		t.Fatal("missing credential fingerprinted")
	}
	if w := fetch(s, "/secret-admin/api/subscription/abcdefghijklmnopqrstuvwx", "Bearer dedicated"); w.Code != 204 {
		t.Fatal(w.Code)
	}
	if w := fetch(s.SubOnly(), "/secret-admin/api/subscription/abcdefghijklmnopqrstuvwx", "Bearer dedicated"); w.Code != 418 {
		t.Fatal("relay escaped onto subscription port")
	}
}
