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

// certAPI is the settings API with counters for what it asks of the certificates.
type certAPI struct {
	t       *testing.T
	handler http.Handler
	token   string
	csrf    string
	set     *settings.Settings
	renewed int
	woken   int
}

func newCertAPI(t *testing.T) *certAPI {
	t.Helper()
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
	if _, err := st.Q.CreateAdmin(ctx, db.CreateAdminParams{Username: "admin", PasswordHash: "x", CreatedAt: now.Unix()}); err != nil {
		t.Fatal(err)
	}
	sessions := auth.NewSessions(st.Q, clock, nil)
	token, sess, err := sessions.Create(ctx, 1, "127.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	c := &certAPI{t: t, token: token, csrf: sess.CsrfToken, set: settings.New(st.Q)}
	pool := domain.NewPool(st, clock)
	c.handler, _, err = New(Deps{
		Version: "test", Store: st, Sessions: sessions, Now: clock, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		IPLimit: auth.NewLimiter(10, time.Minute, time.Minute, time.Hour), UserLimit: auth.NewLimiter(10, time.Minute, time.Minute, time.Hour), TOTP: auth.NewTOTPGuard(),
		Users: domain.NewUsers(st, pool, noChanges{}, clock), Pool: pool, Changes: noChanges{},
		Settings: c.set, RenewCert: func() { c.renewed++ }, WakeNodeCerts: func() { c.woken++ },
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (c *certAPI) patch(body string) *httptest.ResponseRecorder {
	c.t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/settings", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: c.token})
	req.Header.Set("X-CSRF-Token", c.csrf)
	rec := httptest.NewRecorder()
	c.handler.ServeHTTP(rec, req)
	return rec
}

func (c *certAPI) ok(body string) map[string]any {
	c.t.Helper()
	rec := c.patch(body)
	if rec.Code != http.StatusOK {
		c.t.Fatalf("%s: %d %s", body, rec.Code, rec.Body)
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out
}

// A new domain or address gets its certificate at once (GitHub issue #67): the manager's
// own check comes every six hours.
func TestAddressChangeRenewsTheCertificate(t *testing.T) {
	c := newCertAPI(t)
	c.ok(`{"brand":"X"}`)
	if c.renewed != 0 {
		t.Fatalf("the brand has nothing to do with the certificate: %d", c.renewed)
	}
	c.ok(`{"public_host":"203.0.113.10"}`)
	c.ok(`{"domain":""}`)
	if c.renewed != 2 || c.woken != 0 {
		t.Fatalf("an address and a domain saved, %d renewals, %d node wakes", c.renewed, c.woken)
	}
}

// The CA is chosen in the panel: ZeroSSL needs an e-mail, Google an EAB key, which is kept
// and never shown back. A change reissues the panel's and the nodes' certificates.
func TestChoosingTheCA(t *testing.T) {
	c := newCertAPI(t)
	if v := c.ok(`{"brand":"X"}`); v["acme_ca"] != "letsencrypt" || v["acme_eab_hmac_set"] != false {
		t.Fatalf("default: %v %v", v["acme_ca"], v["acme_eab_hmac_set"])
	}
	for body, code := range map[string]string{
		`{"acme_ca":"zerossl"}`: "zerossl_email_required",
		`{"acme_ca":"google"}`:  "google_eab_required",
		`{"acme_ca":"google","acme_eab_kid":"kid","acme_eab_hmac":"short"}`: "eab_hmac_invalid",
		`{"acme_ca":"zerossl","acme_email":"Admin <admin@example.com>"}`:    "email_invalid",
		`{"acme_ca":"other"}`: "",
		`{"acme_ca":"google","acme_eab_kid":"with space","acme_eab_hmac":"` + hmac + `"}`: "eab_kid_invalid",
	} {
		rec := c.patch(body)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), code) {
			t.Errorf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	if c.renewed != 0 || c.woken != 0 {
		t.Fatalf("a refused change reissued: %d %d", c.renewed, c.woken)
	}
	v := c.ok(`{"acme_ca":"zerossl","acme_email":"admin@example.com"}`)
	if v["acme_ca"] != "zerossl" || v["acme_email"] != "admin@example.com" || c.renewed != 1 || c.woken != 1 {
		t.Fatalf("zerossl: %v %v, %d %d", v["acme_ca"], v["acme_email"], c.renewed, c.woken)
	}
	rec := c.patch(`{"acme_ca":"google","acme_eab_kid":"kid-1","acme_eab_hmac":"` + hmac + `"}`)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), hmac) || !strings.Contains(rec.Body.String(), `"acme_eab_hmac_set":true`) {
		t.Fatalf("google: %d %s", rec.Code, rec.Body)
	}
	if saved, _ := c.set.String(context.Background(), settings.KeyACMEEABHMAC); saved != hmac {
		t.Fatalf("the key is not kept: %q", saved)
	}
	// Leaving the CA alone keeps the saved key: the form sends no HMAC it never saw.
	c.ok(`{"acme_eab_kid":"kid-2"}`)
	if saved, _ := c.set.String(context.Background(), settings.KeyACMEEABHMAC); saved != hmac {
		t.Fatalf("the key went with another field: %q", saved)
	}
}

const hmac = "c2VjcmV0LWhtYWMta2V5LTEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2"
