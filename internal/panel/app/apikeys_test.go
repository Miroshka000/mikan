package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"strconv"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/store/db"
)

// API keys: made only from a browser session, shown once, scoped, expiring and revocable;
// a key never reaches the auth and api-keys endpoints.
func TestAPIKeys(t *testing.T) {
	h := newHarness(t)
	api := "/" + adminPath + "/api/v1"
	bearer := func(k string) map[string]string { return map[string]string{"Authorization": "Bearer " + k} }

	// No key and no session: nothing.
	if resp, _ := h.do(http.MethodPost, api+"/api-keys", map[string]any{"name": "x", "scope": "full"}, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous create: %d", resp.StatusCode)
	}
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	// A session still needs the CSRF header to make a key.
	if resp, _ := h.do(http.MethodPost, api+"/api-keys", map[string]any{"name": "x", "scope": "full"}, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("create without csrf: %d", resp.StatusCode)
	}
	type created struct {
		ID     int64  `json:"id"`
		Key    string `json:"key"`
		Prefix string `json:"prefix"`
	}
	mk := func(name, scope string, days int) created {
		t.Helper()
		resp, body := h.do(http.MethodPost, api+"/api-keys", map[string]any{"name": name, "scope": scope, "expire_days": days, "password": password}, csrf)
		var c created
		if resp.StatusCode != http.StatusCreated || json.Unmarshal(body, &c) != nil || !strings.HasPrefix(c.Key, "mk_") || !strings.HasPrefix(c.Key, c.Prefix) {
			t.Fatalf("create %s: %d %s", name, resp.StatusCode, body)
		}
		return c
	}
	for _, bad := range []map[string]any{{"name": " ", "scope": "full", "password": password}, {"name": "x", "scope": "admin", "password": password}, {"name": "x", "scope": "full", "expire_days": -1, "password": password}} {
		if resp, body := h.do(http.MethodPost, api+"/api-keys", bad, csrf); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("%v accepted: %d %s", bad, resp.StatusCode, body)
		}
	}
	read, full, short := mk("grafana", "read", 0), mk("billing", "full", 0), mk("tmp", "full", 1)

	// The list never shows the key or its hash again.
	_, body := h.do(http.MethodGet, api+"/api-keys", nil, nil)
	if strings.Contains(string(body), read.Key) || strings.Contains(string(body), full.Key) || strings.Contains(string(body), "hash") {
		t.Fatalf("list leaks keys: %s", body)
	}

	// Keys work without the session cookie and without CSRF.
	h.client.Jar = nil
	if resp, body := h.do(http.MethodGet, api+"/users", nil, bearer(read.Key)); resp.StatusCode != http.StatusOK {
		t.Fatalf("read key GET: %d %s", resp.StatusCode, body)
	}
	if resp, _ := h.do(http.MethodPatch, api+"/settings", map[string]any{"brand": "X"}, bearer(read.Key)); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("read key may not write: %d", resp.StatusCode)
	}
	if resp, body := h.do(http.MethodPatch, api+"/settings", map[string]any{"brand": "Keyed"}, bearer(full.Key)); resp.StatusCode != http.StatusOK {
		t.Fatalf("full key PATCH: %d %s", resp.StatusCode, body)
	}
	// Session-only endpoints refuse any key.
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/auth/me"}, {http.MethodGet, "/auth/sessions"}, {http.MethodPost, "/auth/password"},
		{http.MethodGet, "/api-keys"}, {http.MethodPost, "/api-keys"}, {http.MethodDelete, "/api-keys/" + strconv.FormatInt(read.ID, 10)},
	} {
		if resp, _ := h.do(c.method, api+c.path, map[string]any{}, bearer(full.Key)); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s %s with a key: %d", c.method, c.path, resp.StatusCode)
		}
	}
	// Wrong, malformed and expired keys.
	for _, hdr := range []map[string]string{
		bearer(full.Key + "x"), bearer("mk_" + strings.Repeat("a", 40)), bearer(""), {"Authorization": "Basic " + full.Key}, bearer(strings.Repeat("z", 4096)),
	} {
		if resp, _ := h.do(http.MethodGet, api+"/users", nil, hdr); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%q: %d", hdr["Authorization"][:min(20, len(hdr["Authorization"]))], resp.StatusCode)
		}
	}
	h.advance(25 * time.Hour)
	if resp, _ := h.do(http.MethodGet, api+"/users", nil, bearer(short.Key)); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired key: %d", resp.StatusCode)
	}

	// What a key did is in the audit log under its prefix.
	entries, err := h.st.Q.ListAudit(context.Background(), db.ListAuditParams{BeforeID: 1 << 62, Lim: 100})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if e.Action == "settings.update" && strings.Contains(e.Details.String, full.Prefix) {
			found = true
		}
	}
	if !found {
		t.Fatalf("audit has no api key: %+v", entries)
	}

	// Revoked: gone at once.
	h.client.Jar, _ = cookiejar.New(nil)
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("relogin")
	}
	csrf = map[string]string{"X-CSRF-Token": h.csrf}
	if resp, _ := h.do(http.MethodDelete, api+"/api-keys/"+strconv.FormatInt(full.ID, 10), nil, csrf); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke: %d", resp.StatusCode)
	}
	if resp, _ := h.do(http.MethodGet, api+"/users", nil, bearer(full.Key)); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked key: %d", resp.StatusCode)
	}

	// Guessing gets the IP blocked, valid keys included.
	for range 12 {
		h.do(http.MethodGet, api+"/users", nil, bearer("mk_guess"))
	}
	if resp, _ := h.do(http.MethodGet, api+"/users", nil, bearer(read.Key)); resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("after guessing: %d", resp.StatusCode)
	}
}
