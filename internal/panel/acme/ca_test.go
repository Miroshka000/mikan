package acme

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"mikan/internal/acmechallenge"
	"mikan/internal/panel/settings"
)

// newTestChallenge answers on a free loopback port instead of port 80.
func newTestChallenge(t *testing.T) *acmechallenge.Server {
	s := acmechallenge.New("127.0.0.1:0")
	t.Cleanup(s.Close)
	return s
}

// zeroSSL is ZeroSSL's EAB API: credentials for the e-mail, or its error.
func zeroSSL(t *testing.T, answer func(email string) (int, string)) *[]string {
	t.Helper()
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		_ = r.ParseForm()
		asked = append(asked, r.PostForm.Get("email"))
		code, body := answer(r.PostForm.Get("email"))
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	old := zeroSSLAPI
	zeroSSLAPI = srv.URL
	t.Cleanup(func() { zeroSSLAPI = old })
	return &asked
}

func TestZeroSSLEAB(t *testing.T) {
	asked := zeroSSL(t, func(email string) (int, string) {
		if email == "admin@example.com" {
			return 200, `{"success":true,"eab_kid":"kid-1","eab_hmac_key":"c2VjcmV0LWhtYWMta2V5LTEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2"}`
		}
		return 400, `{"success":false,"error":{"code":2900,"type":"invalid_email"}}`
	})
	eab, err := zeroSSLEAB(context.Background(), http.DefaultClient, "admin@example.com")
	if err != nil || eab.KID != "kid-1" || eab.HMAC != "c2VjcmV0LWhtYWMta2V5LTEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2" {
		t.Fatalf("eab %+v, %v", eab, err)
	}
	if _, err := zeroSSLEAB(context.Background(), http.DefaultClient, "bad@"); !errors.Is(err, ErrZeroSSLEAB) {
		t.Fatalf("a refused e-mail: %v", err)
	}
	if _, err := zeroSSLEAB(context.Background(), http.DefaultClient, ""); !errors.Is(err, ErrEmailRequired) {
		t.Fatalf("no e-mail: %v", err)
	}
	if len(*asked) != 2 {
		t.Fatalf("the API was asked %v", *asked)
	}
}

// Choosing ZeroSSL reissues: the Let's Encrypt certificate is served until ZeroSSL's comes,
// and the account is bound with the EAB ZeroSSL hands out for the admin's e-mail.
func TestSwitchingToZeroSSLReissues(t *testing.T) {
	zeroSSL(t, func(string) (int, string) {
		return 200, `{"success":true,"eab_kid":"zkid","eab_hmac_key":"c2VjcmV0LWhtYWMta2V5LTEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2"}`
	})
	ca := newFakeCA(t, func(string, string, string) bool { return true })
	m, set := testManager(t, "vpn.example.com")
	ctx := context.Background()
	// Let's Encrypt first (the fake CA accepts any challenge).
	m.challenge = newTestChallenge(t)
	if st, done := m.RenewNow(ctx, 30*time.Second); !done || st.Kind != "acme" || st.CA != CALetsEncrypt {
		t.Fatalf("first: %+v", st)
	}
	// ZeroSSL now requires the binding.
	ca.mu.Lock()
	ca.requireEAB, ca.account = true, false
	ca.mu.Unlock()
	if err := settings.Set(ctx, set, settings.KeyACMECA, CAZeroSSL); err != nil {
		t.Fatal(err)
	}
	m.Load(ctx)
	if st := m.Status(); st.Kind != "acme" || st.CA != CALetsEncrypt || st.WantCA != CAZeroSSL {
		t.Fatalf("while switching the old certificate is served: %+v", st)
	}
	// Without an e-mail ZeroSSL cannot be had.
	if st, _ := m.RenewNow(ctx, 30*time.Second); st.Error != ErrEmailRequired.Error() || st.Kind != "acme" {
		t.Fatalf("no e-mail: %+v", st)
	}
	if err := settings.Set(ctx, set, settings.KeyACMEEmail, "admin@example.com"); err != nil {
		t.Fatal(err)
	}
	st, done := m.RenewNow(ctx, 30*time.Second)
	if !done || st.Kind != "acme" || st.CA != CAZeroSSL || st.Error != "" {
		t.Fatalf("zerossl: %+v", st)
	}
	if ca.eabKID != "zkid" {
		t.Fatalf("bound to %q", ca.eabKID)
	}
}

// Google needs the admin's EAB key: without it the order says so.
func TestGoogleWithoutEAB(t *testing.T) {
	ca := newFakeCA(t, func(string, string, string) bool { return true })
	ca.requireEAB = true
	m, set := testManager(t, "vpn.example.com")
	m.challenge = newTestChallenge(t)
	ctx := context.Background()
	if err := settings.Set(ctx, set, settings.KeyACMECA, CAGoogle); err != nil {
		t.Fatal(err)
	}
	if st, _ := m.RenewNow(ctx, 30*time.Second); st.Error != ErrGoogleEAB.Error() {
		t.Fatalf("no EAB: %+v", st)
	}
	_ = settings.Set(ctx, set, settings.KeyACMEEABKID, "gkid")
	_ = settings.Set(ctx, set, settings.KeyACMEEABHMAC, "c2VjcmV0LWhtYWMta2V5LTEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2")
	if st, _ := m.RenewNow(ctx, 30*time.Second); st.Error != "" || st.CA != CAGoogle || ca.eabKID != "gkid" {
		t.Fatalf("with EAB: %+v, kid %q", st, ca.eabKID)
	}
}

func TestEffectiveCA(t *testing.T) {
	for _, c := range []struct{ chosen, id, want string }{
		{"", "vpn.example.com", CALetsEncrypt},
		{CAZeroSSL, "vpn.example.com", CAZeroSSL},
		{CAGoogle, "vpn.example.com", CAGoogle},
		{CAZeroSSL, "203.0.113.5", CALetsEncrypt},
		{CAGoogle, "2001:db8::1", CALetsEncrypt},
		{"other", "vpn.example.com", CALetsEncrypt},
	} {
		if got := EffectiveCA(c.chosen, c.id); got != c.want {
			t.Errorf("%s for %s: %s", c.chosen, c.id, got)
		}
	}
}

func TestEABValidation(t *testing.T) {
	if !ValidEABKey("c2VjcmV0LWhtYWMta2V5LTEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2") || ValidEABKey("short") || ValidEABKey("not base64!!") {
		t.Fatal("hmac")
	}
	if !ValidEABKID("kid-1_x") || ValidEABKID("") || ValidEABKID("with space") {
		t.Fatal("kid")
	}
	if !ValidEmail("admin@example.com") || ValidEmail("Admin <admin@example.com>") || ValidEmail("nope") {
		t.Fatal("email")
	}
}
