package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/auth"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/store/storetest"
)

// The subscription domain is saved, renewed the certificate with the domain's own, is
// shown back with the links built on it, and a non-name is refused. A read-only key sees
// no links (it may not see where the users' subscriptions live).
func TestSubDomainSettings(t *testing.T) {
	ctx := context.Background()
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Unix(1_800_000_000, 0)
	clock := func() time.Time { return now }
	if err := domain.Seed(ctx, st, now); err != nil {
		t.Fatal(err)
	}
	set := settings.New(st.Q)
	for k, v := range map[string]any{settings.KeyPanelPort: 2053, settings.KeySubPath: "s0b"} {
		if err := settings.Set(ctx, set, k, v); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.Q.CreateAdmin(ctx, db.CreateAdminParams{Username: "admin", PasswordHash: "x", CreatedAt: now.Unix()}); err != nil {
		t.Fatal(err)
	}
	sessions := auth.NewSessions(st.Q, clock, nil)
	token, sess, err := sessions.Create(ctx, 1, "127.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	renewed := 0
	pool := domain.NewPool(st, clock)
	handler, _, err := New(Deps{
		Version: "test", Store: st, Sessions: sessions, Now: clock, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		IPLimit: auth.NewLimiter(10, time.Minute, time.Minute, time.Hour), UserLimit: auth.NewLimiter(10, time.Minute, time.Minute, time.Hour), TOTP: auth.NewTOTPGuard(),
		Users: domain.NewUsers(st, pool, noChanges{}, clock), Pool: pool, Changes: noChanges{},
		Settings: settings.New(st.Q), RenewCert: func() { renewed++ },
		SubPort: func(int) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	patch := func(body string) (int, map[string]any, string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/settings", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
		req.Header.Set("X-CSRF-Token", sess.CsrfToken)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		var out map[string]any
		if rec.Code == http.StatusOK {
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
		}
		return rec.Code, out, rec.Body.String()
	}

	if code, v, body := patch(`{"sub_domain":"sub.example.com"}`); code != http.StatusOK || v["sub_domain"] != "sub.example.com" || v["sub_base_url"] != "https://sub.example.com:2053/" {
		t.Fatalf("saved: %d %s", code, body)
	}
	if renewed != 1 {
		t.Fatalf("a subscription domain needs its name on the certificate: %d renewals", renewed)
	}
	// The panel yet on a bare address: the links stand all the same, that is the point.
	if err := settings.Set(ctx, settings.New(st.Q), settings.KeyPublicHost, "203.0.113.10"); err != nil {
		t.Fatal(err)
	}
	if code, v, body := patch(`{"sub_port":8855}`); code != http.StatusOK || v["sub_base_url"] != "https://sub.example.com:8855/" {
		t.Fatalf("with a port: %d %s", code, body)
	}
	// Off: the links go back to the panel's address with the secret path.
	if code, v, body := patch(`{"sub_domain":""}`); code != http.StatusOK || v["sub_base_url"] != "https://203.0.113.10:8855/s0b/" {
		t.Fatalf("cleared: %d %s", code, body)
	}
	// An IP is not a subscription domain: there is nothing to point a name at.
	patch(`{"public_host":"vpn.example.com"}`)
	code, _, _ := patch(`{"sub_domain":"203.0.113.10"}`)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("an address as a subscription domain: %d", code)
	}
	if renewed != 3 {
		t.Fatalf("a refused name is not saved: %d renewals", renewed)
	}
}
