package app

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"

	"github.com/descope/virtualwebauthn"

	"mikan/internal/panel/settings"
)

// passkeyRP is the relying party the panel of the tests is for: its domain on 443.
var passkeyRP = virtualwebauthn.RelyingParty{ID: "panel.example", Name: "mikan", Origin: "https://panel.example"}

func passkeyHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	set := settings.New(h.st.Q)
	if err := settings.Set(context.Background(), set, settings.KeyDomain, passkeyRP.ID); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(context.Background(), set, settings.KeyPanelPort, 443); err != nil {
		t.Fatal(err)
	}
	return h
}

// adminDevice is the admin's authenticator: it answers for the admin of id 1, with user
// verification unless told otherwise.
func adminDevice(opts virtualwebauthn.AuthenticatorOptions) virtualwebauthn.Authenticator {
	opts.UserHandle = binary.BigEndian.AppendUint64(nil, 1)
	return virtualwebauthn.NewAuthenticatorWithOptions(opts)
}

// addPasskey runs the ceremony of adding a passkey from the signed-in session of h.
func (h *harness) addPasskey(rp virtualwebauthn.RelyingParty, dev virtualwebauthn.Authenticator, cred virtualwebauthn.Credential) (*http.Response, []byte) {
	h.t.Helper()
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	resp, body := h.do(http.MethodPost, "/"+adminPath+"/api/v1/auth/passkeys/begin", map[string]string{"password": password}, csrf)
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("begin: %d %s", resp.StatusCode, body)
	}
	var begin struct {
		Options json.RawMessage `json:"options"`
	}
	if err := json.Unmarshal(body, &begin); err != nil {
		h.t.Fatal(err)
	}
	opts, err := virtualwebauthn.ParseAttestationOptions(string(begin.Options))
	if err != nil {
		h.t.Fatal(err)
	}
	if opts.RelyingPartyID != passkeyRP.ID {
		h.t.Fatalf("rp id %q", opts.RelyingPartyID)
	}
	att := virtualwebauthn.CreateAttestationResponse(rp, dev, cred, *opts)
	return h.do(http.MethodPost, "/"+adminPath+"/api/v1/auth/passkeys", map[string]any{"name": "Phone", "credential": json.RawMessage(att)}, csrf)
}

// passkeyLogin signs in with a passkey from a browser of its own, which holds no session.
func (h *harness) passkeyLogin(rp virtualwebauthn.RelyingParty, dev virtualwebauthn.Authenticator, cred virtualwebauthn.Credential) (*http.Response, []byte, *http.Client) {
	h.t.Helper()
	c := h.browser()
	resp, body := h.post(c, "/api/v1/auth/login/passkey", h.passkeyAnswer(c, rp, dev, cred))
	return resp, body, c
}

// browser is a client of the test server with cookies of its own (h.ts.Client is the one
// client of h, whose jar holds the admin's session).
func (h *harness) browser() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Transport: h.ts.Client().Transport, Jar: jar}
}

// passkeyAnswer begins a sign-in from c and gives the device's answer to it.
func (h *harness) passkeyAnswer(c *http.Client, rp virtualwebauthn.RelyingParty, dev virtualwebauthn.Authenticator, cred virtualwebauthn.Credential) map[string]any {
	h.t.Helper()
	resp, body := h.post(c, "/api/v1/auth/login/passkey/begin", nil)
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("login begin: %d %s", resp.StatusCode, body)
	}
	var begin struct {
		Ceremony string          `json:"ceremony"`
		Options  json.RawMessage `json:"options"`
	}
	if err := json.Unmarshal(body, &begin); err != nil {
		h.t.Fatal(err)
	}
	opts, err := virtualwebauthn.ParseAssertionOptions(string(begin.Options))
	if err != nil {
		h.t.Fatal(err)
	}
	if len(opts.AllowCredentials) != 0 || opts.RelyingPartyID != passkeyRP.ID {
		h.t.Fatalf("a sign-in for %q that names keys: %v", opts.RelyingPartyID, opts.AllowCredentials)
	}
	assertion := virtualwebauthn.CreateAssertionResponse(rp, dev, cred, *opts)
	return map[string]any{"ceremony": begin.Ceremony, "credential": json.RawMessage(assertion)}
}

func (h *harness) post(c *http.Client, path string, body any) (*http.Response, []byte) {
	h.t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := c.Post(h.ts.URL+"/"+adminPath+path, "application/json", bytes.NewReader(raw))
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, out
}

func TestPasskeyAddAndSignIn(t *testing.T) {
	h := passkeyHarness(t)
	if resp, body := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d %s", resp.StatusCode, body)
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	// Adding a key asks for the password first.
	if resp, body := h.do(http.MethodPost, "/"+adminPath+"/api/v1/auth/passkeys/begin", map[string]string{"password": "wrong"}, csrf); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "wrong_password") {
		t.Fatalf("begin, wrong password: %d %s", resp.StatusCode, body)
	}

	dev := adminDevice(virtualwebauthn.AuthenticatorOptions{})
	cred := virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2)
	// A key made for another site is not the panel's.
	other := passkeyRP
	other.Origin = "https://evil.example"
	if resp, body := h.addPasskey(other, dev, cred); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "invalid_passkey") {
		t.Fatalf("add from another origin: %d %s", resp.StatusCode, body)
	}
	// Nor one that did not check who holds the device.
	if resp, body := h.addPasskey(passkeyRP, adminDevice(virtualwebauthn.AuthenticatorOptions{UserNotVerified: true}), cred); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("add with no user verification: %d %s", resp.StatusCode, body)
	}
	resp, body := h.addPasskey(passkeyRP, dev, cred)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add: %d %s", resp.StatusCode, body)
	}
	dev.AddCredential(cred)
	// The ceremony is spent: its answer is not taken twice.
	if resp, body := h.do(http.MethodPost, "/"+adminPath+"/api/v1/auth/passkeys", map[string]any{"name": "Again", "credential": json.RawMessage(`{}`)}, csrf); resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "passkey_setup_expired") {
		t.Fatalf("add again: %d %s", resp.StatusCode, body)
	}

	resp, c := h.passkeySignIn(dev, cred)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("passkey sign-in: %d", resp.StatusCode)
	}
	me, err := c.Get(h.ts.URL + "/" + adminPath + "/api/v1/auth/me")
	if err != nil {
		t.Fatal(err)
	}
	me.Body.Close()
	if me.StatusCode != http.StatusOK {
		t.Fatalf("me after a passkey sign-in: %d", me.StatusCode)
	}
	var list []struct {
		ID         int64   `json:"id"`
		Name       string  `json:"name"`
		LastUsedAt *string `json:"last_used_at"`
	}
	resp, body = h.do(http.MethodGet, "/"+adminPath+"/api/v1/auth/passkeys", nil, nil)
	if err := json.Unmarshal(body, &list); err != nil || resp.StatusCode != http.StatusOK || len(list) != 1 || list[0].Name != "Phone" || list[0].LastUsedAt == nil {
		t.Fatalf("list: %d %s", resp.StatusCode, body)
	}

	// An answer signed for another origin, or by a key the panel does not have, lets nobody in.
	if resp, body, _ := h.passkeyLogin(other, dev, cred); resp.StatusCode != http.StatusUnauthorized || !strings.Contains(string(body), "invalid_passkey") {
		t.Fatalf("sign-in from another origin: %d %s", resp.StatusCode, body)
	}
	stranger := virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2)
	if resp, body, _ := h.passkeyLogin(passkeyRP, dev, stranger); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("sign-in with an unknown key: %d %s", resp.StatusCode, body)
	}

	// A deleted key signs in no more.
	if resp, body := h.do(http.MethodDelete, "/"+adminPath+"/api/v1/auth/passkeys/"+jsonInt(list[0].ID), nil, csrf); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d %s", resp.StatusCode, body)
	}
	if resp, _ := h.passkeySignIn(dev, cred); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("sign-in with a deleted key: %d", resp.StatusCode)
	}
}

// passkeySignIn is a sign-in with the key that moves the device's counter, as a real one does.
func (h *harness) passkeySignIn(dev virtualwebauthn.Authenticator, cred virtualwebauthn.Credential) (*http.Response, *http.Client) {
	h.t.Helper()
	cred.Counter++
	resp, _, c := h.passkeyLogin(passkeyRP, dev, cred)
	return resp, c
}

func jsonInt(v int64) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

// A sign-in's ceremony is used once, and a counter that went back says the key was copied.
func TestPasskeySignInReplay(t *testing.T) {
	h := passkeyHarness(t)
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	dev := adminDevice(virtualwebauthn.AuthenticatorOptions{})
	cred := virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2)
	cred.Counter = 5
	if resp, body := h.addPasskey(passkeyRP, dev, cred); resp.StatusCode != http.StatusCreated {
		t.Fatalf("add: %d %s", resp.StatusCode, body)
	}

	cred.Counter = 6
	c := h.browser()
	answer := h.passkeyAnswer(c, passkeyRP, dev, cred)
	if resp, body := h.post(c, "/api/v1/auth/login/passkey", answer); resp.StatusCode != http.StatusOK {
		t.Fatalf("sign-in: %d %s", resp.StatusCode, body)
	}
	// The same answer again: the ceremony it was for is spent.
	if resp, body := h.post(c, "/api/v1/auth/login/passkey", answer); resp.StatusCode != http.StatusUnauthorized || !strings.Contains(string(body), "passkey_expired") {
		t.Fatalf("a replayed answer: %d %s", resp.StatusCode, body)
	}

	// The counter is back at 3: a copy of the key.
	cred.Counter = 3
	if resp, body, _ := h.passkeyLogin(passkeyRP, dev, cred); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a counter that went back: %d %s", resp.StatusCode, body)
	}
}

// Without the panel's domain there are no passkeys: an IP address cannot hold one.
func TestPasskeyNeedsDomain(t *testing.T) {
	h := newHarness(t)
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	resp, body := h.do(http.MethodPost, "/"+adminPath+"/api/v1/auth/passkeys/begin", map[string]string{"password": password}, map[string]string{"X-CSRF-Token": h.csrf})
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "passkey_no_domain") {
		t.Fatalf("begin with no domain: %d %s", resp.StatusCode, body)
	}
	resp, body = h.do(http.MethodPost, "/"+adminPath+"/api/v1/auth/login/passkey/begin", nil, nil)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "passkey_no_domain") {
		t.Fatalf("sign-in with no domain: %d %s", resp.StatusCode, body)
	}
}

// A new password ends the passkeys with the API keys (revoke_keys, on by default): one added
// from a hijacked session would otherwise outlive the reaction to it. Kept, they still work.
func TestPasswordChangeRevokesPasskeys(t *testing.T) {
	for _, keep := range []bool{false, true} {
		h := passkeyHarness(t)
		if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
			t.Fatal("login")
		}
		dev := adminDevice(virtualwebauthn.AuthenticatorOptions{})
		cred := virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2)
		if resp, body := h.addPasskey(passkeyRP, dev, cred); resp.StatusCode != http.StatusCreated {
			t.Fatalf("add: %d %s", resp.StatusCode, body)
		}
		body := map[string]any{"current": password, "new": "another-long-password"}
		if keep {
			body["revoke_keys"] = false
		}
		if resp, out := h.do(http.MethodPost, "/"+adminPath+"/api/v1/auth/password", body, map[string]string{"X-CSRF-Token": h.csrf}); resp.StatusCode != http.StatusNoContent {
			t.Fatalf("change password: %d %s", resp.StatusCode, out)
		}
		resp, _ := h.passkeySignIn(dev, cred)
		if keep && resp.StatusCode != http.StatusOK {
			t.Errorf("kept passkeys: the passkey stopped working: %d", resp.StatusCode)
		}
		if !keep && resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("revoked passkeys: the passkey still signs in: %d", resp.StatusCode)
		}
		if rows, _ := h.st.Q.ListAdminPasskeys(context.Background(), 1); (len(rows) != 0) != keep {
			t.Errorf("keep=%v: %d passkeys left", keep, len(rows))
		}
	}
}

// A device that did not check who holds it signs nobody in, though the key is the admin's.
func TestPasskeySignInNeedsUserVerification(t *testing.T) {
	h := passkeyHarness(t)
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	cred := virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2)
	if resp, body := h.addPasskey(passkeyRP, adminDevice(virtualwebauthn.AuthenticatorOptions{}), cred); resp.StatusCode != http.StatusCreated {
		t.Fatalf("add: %d %s", resp.StatusCode, body)
	}
	cred.Counter = 1
	if resp, body, _ := h.passkeyLogin(passkeyRP, adminDevice(virtualwebauthn.AuthenticatorOptions{UserNotVerified: true}), cred); resp.StatusCode != http.StatusUnauthorized || !strings.Contains(string(body), "invalid_passkey") {
		t.Fatalf("sign-in with no user verification: %d %s", resp.StatusCode, body)
	}
	cred.Counter = 2
	if resp, body, _ := h.passkeyLogin(passkeyRP, adminDevice(virtualwebauthn.AuthenticatorOptions{}), cred); resp.StatusCode != http.StatusOK {
		t.Fatalf("sign-in with user verification: %d %s", resp.StatusCode, body)
	}
}

// An API key reaches no passkey endpoint of the admin: a leaked key must not add a way in.
func TestKeyCannotManagePasskeys(t *testing.T) {
	k := newKeyHarness(t)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/auth/passkeys"}, {http.MethodPost, "/auth/passkeys/begin"},
		{http.MethodPost, "/auth/passkeys"}, {http.MethodDelete, "/auth/passkeys/1"},
	} {
		if resp, body := k.asKey(k.full, c.method, c.path, map[string]any{"password": password, "name": "k", "credential": map[string]any{}}); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s with a key: %d %s, want 403", c.method, c.path, resp.StatusCode, body)
		}
	}
}
