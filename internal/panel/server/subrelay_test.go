package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"mikan/internal/panel/settings"
)

// The relay lives under the subscription path: without the dedicated key it answers as
// any other path there that the subscriptions do not know.
func TestRelayAnswersLikeOtherSubscriptionPaths(t *testing.T) {
	sub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404); w.Write([]byte("subscriptions: " + r.URL.Path)) })
	s := New(http.NotFoundHandler(), sub)
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
	// What the subscriptions' handler makes of a path under them it does not know.
	unknown := func(w *httptest.ResponseRecorder) bool {
		return w.Code == 404 && w.Body.String() == "subscriptions: /relay/abcdefghijklmnopqrstuvwx"
	}
	for _, auth := range []string{"", "Bearer dedicated"} {
		if w := fetch(s, "/secret-sub/relay/abcdefghijklmnopqrstuvwx", auth); !unknown(w) {
			t.Fatalf("disabled endpoint fingerprinted: %d %s", w.Code, w.Body)
		}
	}
	enabled = true
	if w := fetch(s, "/secret-sub/relay/abcdefghijklmnopqrstuvwx", ""); !unknown(w) {
		t.Fatalf("missing credential fingerprinted: %d %s", w.Code, w.Body)
	}
	if w := fetch(s, "/secret-sub/relay/abcdefghijklmnopqrstuvwx", "Bearer dedicated"); w.Code != 204 {
		t.Fatal(w.Code)
	}
	if w := fetch(s.SubOnly(), "/secret-sub/relay/abcdefghijklmnopqrstuvwx", "Bearer dedicated"); w.Code != 404 {
		t.Fatal("relay escaped onto subscription port")
	}
}
