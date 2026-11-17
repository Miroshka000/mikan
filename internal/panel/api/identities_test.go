package api

import (
	"context"
	chmac "crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"mikan/internal/panel/auth"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/tgate"
	"mikan/internal/tgapi"
)

const identityPassword = "right-password-123"

// newIdentityFixture is the host fixture with a real password and the panel at
// panel.example.com:8443.
func newIdentityFixture(t *testing.T) *hostFixture {
	t.Helper()
	f := newHostFixture(t, false)
	ctx := context.Background()
	hash, err := auth.HashPassword(identityPassword)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetAdminPassword(ctx, db.SetAdminPasswordParams{PasswordHash: hash, ID: 1}); err != nil {
		t.Fatal(err)
	}
	for key, v := range map[string]any{settings.KeyDomain: "panel.example.com", settings.KeyPanelPort: 8443} {
		if err := settings.Set(ctx, f.deps.Settings, key, v); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// public sends a request with no session, as a browser that is not signed in.
func (f *hostFixture) public(method, path, body string) *httptest.ResponseRecorder {
	f.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.RemoteAddr = "198.51.100.7:5000"
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

func stateOf(t *testing.T, body string) string {
	t.Helper()
	var out struct{ URL string }
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(out.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get("state")
}

// fakeGitHub answers the code "good" with the account octo (42) and "other" with 43.
func fakeGitHub(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = r.ParseForm()
			if r.Form.Get("client_secret") != "sec" || !strings.HasSuffix(r.Form.Get("redirect_uri"), "/api/v1/auth/github/callback") {
				_, _ = w.Write([]byte(`{"error":"bad_verification_code"}`))
				return
			}
			_, _ = w.Write([]byte(`{"access_token":"tok-` + r.Form.Get("code") + `"}`))
		case "/user":
			switch r.Header.Get("Authorization") {
			case "Bearer tok-good":
				_, _ = w.Write([]byte(`{"id":42,"login":"octo"}`))
			case "Bearer tok-other":
				_, _ = w.Write([]byte(`{"id":43,"login":"stranger"}`))
			default:
				w.WriteHeader(http.StatusUnauthorized)
			}
		}
	}))
	t.Cleanup(srv.Close)
	tokenURL, userURL := githubTokenURL, githubUserURL
	githubTokenURL, githubUserURL = srv.URL+"/token", srv.URL+"/user"
	t.Cleanup(func() { githubTokenURL, githubUserURL = tokenURL, userURL })
}

// A GitHub account is linked from the admin's session with the password, and then signs in
// through the login page with the code when 2FA is on. The callback answers every outcome
// with the admin's page, the outcome in the fragment.
func TestGitHubIdentity(t *testing.T) {
	f := newIdentityFixture(t)
	fakeGitHub(t)
	const base = "https://panel.example.com:8443/" + hostAdminPath

	if code, body := f.send(http.MethodPost, "/api/v1/auth/identities/github/begin", `{"password": "`+identityPassword+`"}`); code != http.StatusConflict || !strings.Contains(body, "github_off") {
		t.Fatalf("begin without an app: %d %s", code, body)
	}
	code, body := f.send(http.MethodPut, "/api/v1/auth/identity-providers/github", `{"client_id": "cid", "client_secret": "sec"}`)
	if code != http.StatusOK || !strings.Contains(body, `"callback_url":"`+base+`/api/v1/auth/github/callback"`) || !strings.Contains(body, `"secret_set":true`) || strings.Contains(body, "sec\"") {
		t.Fatalf("the app: %d %s", code, body)
	}
	if rec := f.public(http.MethodGet, "/api/v1/auth/login/methods", ""); !strings.Contains(rec.Body.String(), `"github":false`) {
		t.Fatalf("methods before a link: %s", rec.Body.String())
	}
	if code, body := f.send(http.MethodPost, "/api/v1/auth/identities/github/begin", `{"password": "wrong"}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("begin with a wrong password: %d %s", code, body)
	}
	code, body = f.send(http.MethodPost, "/api/v1/auth/identities/github/begin", `{"password": "`+identityPassword+`"}`)
	if code != http.StatusOK || !strings.Contains(body, "client_id=cid") {
		t.Fatalf("begin: %d %s", code, body)
	}
	callback := func(state, code string) string {
		t.Helper()
		rec := f.public(http.MethodGet, "/api/v1/auth/github/callback?code="+code+"&state="+state, "")
		if rec.Code != http.StatusFound {
			t.Fatalf("callback: %d %s", rec.Code, rec.Body.String())
		}
		return rec.Header().Get("Location")
	}
	if got := callback(stateOf(t, body), "good"); got != base+"/settings?tab=security#identity=github" {
		t.Fatalf("linked: %s", got)
	}
	if got := callback(stateOf(t, body), "good"); got != base+"/login#github_error=identity_expired" {
		t.Fatalf("a state used twice: %s", got)
	}
	code, body = f.send(http.MethodGet, "/api/v1/auth/identities", "")
	if code != http.StatusOK || !strings.Contains(body, `"provider":"github"`) || !strings.Contains(body, `"name":"octo"`) {
		t.Fatalf("identities: %d %s", code, body)
	}
	if rec := f.public(http.MethodGet, "/api/v1/auth/login/methods", ""); !strings.Contains(rec.Body.String(), `"github":true`) {
		t.Fatalf("methods after a link: %s", rec.Body.String())
	}
	// One account of each provider per admin.
	_, body = f.send(http.MethodPost, "/api/v1/auth/identities/github/begin", `{"password": "`+identityPassword+`"}`)
	if got := callback(stateOf(t, body), "other"); got != base+"/settings?tab=security#github_error=identity_taken" {
		t.Fatalf("a second account: %s", got)
	}

	ticket := func(code string) string {
		t.Helper()
		rec := f.public(http.MethodPost, "/api/v1/auth/login/github/begin", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("login begin: %d %s", rec.Code, rec.Body.String())
		}
		loc := callback(stateOf(t, rec.Body.String()), code)
		tk, ok := strings.CutPrefix(loc, base+"/login#github=")
		if !ok {
			t.Fatalf("login callback: %s", loc)
		}
		return tk
	}
	login := func(tk, code string) *httptest.ResponseRecorder {
		t.Helper()
		return f.public(http.MethodPost, "/api/v1/auth/login/github", `{"ticket": "`+tk+`", "totp": "`+code+`"}`)
	}
	tk := ticket("good")
	if rec := login(tk, ""); rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Set-Cookie"), auth.CookieName+"=") {
		t.Fatalf("login: %d %s", rec.Code, rec.Body.String())
	}
	if rec := login(tk, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a ticket used twice: %d %s", rec.Code, rec.Body.String())
	}
	// Another address cannot redeem the ticket.
	tk = ticket("good")
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login/github", strings.NewReader(`{"ticket": "`+tk+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "203.0.113.9:5000"
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("a ticket from elsewhere: %d %s", rec.Code, rec.Body.String())
	}
	rec = f.public(http.MethodPost, "/api/v1/auth/login/github/begin", "")
	if got := callback(stateOf(t, rec.Body.String()), "other"); got != base+"/login#github_error=identity_unknown" {
		t.Fatalf("an account nobody linked: %s", got)
	}

	// With 2FA the code is asked, and the ticket waits for it.
	const secret = "JBSWY3DPEHPK3PXP"
	if err := f.st.SetAdminTOTP(context.Background(), db.SetAdminTOTPParams{ID: 1, TotpSecret: sql.NullString{String: secret, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	tk = ticket("good")
	if rec := login(tk, ""); rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "totp_required") {
		t.Fatalf("login without the code: %d %s", rec.Code, rec.Body.String())
	}
	if rec := login(tk, "000000"); rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "invalid_totp") {
		t.Fatalf("login with a wrong code: %d %s", rec.Code, rec.Body.String())
	}
	good, err := totp.GenerateCode(secret, f.deps.Now())
	if err != nil {
		t.Fatal(err)
	}
	if rec := login(tk, good); rec.Code != http.StatusOK {
		t.Fatalf("login with the code: %d %s", rec.Code, rec.Body.String())
	}

	// A new password unlinks the accounts, as it revokes the passkeys.
	if code, body := f.send(http.MethodPost, "/api/v1/auth/password", `{"current": "`+identityPassword+`", "new": "another-password-456"}`); code != http.StatusNoContent {
		t.Fatalf("new password: %d %s", code, body)
	}
	if _, body := f.send(http.MethodGet, "/api/v1/auth/identities", ""); body != "[]" && !strings.HasPrefix(strings.TrimSpace(body), "[]") {
		t.Fatalf("identities after a new password: %s", body)
	}
	if f.audited("auth.identity_added") != 1 || f.audited("auth.login") != 2 {
		t.Fatalf("audit: added %d, logins %d", f.audited("auth.identity_added"), f.audited("auth.login"))
	}
}

// signTelegram is a Telegram login's result for the account id, signed with token.
func signTelegram(token string, id int64, username string, at time.Time) string {
	fields := map[string]string{"id": strconv.FormatInt(id, 10), "first_name": "Ann", "username": username, "auth_date": strconv.FormatInt(at.Unix(), 10)}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := make([]string, len(keys))
	for i, k := range keys {
		lines[i] = k + "=" + fields[k]
	}
	key := sha256.Sum256([]byte(token))
	m := chmac.New(sha256.New, key[:])
	m.Write([]byte(strings.Join(lines, "\n")))
	raw, _ := json.Marshal(map[string]any{"id": id, "first_name": "Ann", "username": username, "auth_date": at.Unix(), "hash": hex.EncodeToString(m.Sum(nil))})
	return base64.RawURLEncoding.EncodeToString(raw)
}

// A Telegram account is linked with the password through the panel's bot and signs in with
// its signed login, each signature once.
func TestTelegramIdentity(t *testing.T) {
	f := newIdentityFixture(t)
	ctx := context.Background()
	const token = "123456:SECRET-token"
	if code, body := f.send(http.MethodPost, "/api/v1/auth/identities/telegram/begin", `{"password": "`+identityPassword+`"}`); code != http.StatusConflict || !strings.Contains(body, "telegram_off") {
		t.Fatalf("begin without a bot: %d %s", code, body)
	}
	if err := settings.Set(ctx, f.deps.Settings, tgate.KeyToken, token); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, f.deps.Settings, tgate.KeyBot, tgapi.User{ID: 123456, Username: "mikanbot"}); err != nil {
		t.Fatal(err)
	}
	if code, body := f.send(http.MethodGet, "/api/v1/auth/identity-providers", ""); code != http.StatusOK || !strings.Contains(body, `"bot":"mikanbot"`) || !strings.Contains(body, `"domain":"panel.example.com"`) {
		t.Fatalf("providers: %d %s", code, body)
	}
	link := `{"result": "` + signTelegram(token, 777, "ann", f.deps.Now()) + `"}`
	if code, body := f.send(http.MethodPost, "/api/v1/auth/identities/telegram", link); code != http.StatusConflict {
		t.Fatalf("a link not begun: %d %s", code, body)
	}
	code, body := f.send(http.MethodPost, "/api/v1/auth/identities/telegram/begin", `{"password": "`+identityPassword+`"}`)
	if code != http.StatusOK || !strings.Contains(body, "bot_id=123456") || !strings.Contains(body, "origin=https%3A%2F%2Fpanel.example.com%3A8443") ||
		!strings.Contains(body, url.QueryEscape("https://panel.example.com:8443/"+hostAdminPath+"/settings?tab=security")) {
		t.Fatalf("begin: %d %s", code, body)
	}
	if code, body := f.send(http.MethodPost, "/api/v1/auth/identities/telegram", link); code != http.StatusCreated || !strings.Contains(body, `"name":"@ann"`) {
		t.Fatalf("link: %d %s", code, body)
	}
	if rec := f.public(http.MethodGet, "/api/v1/auth/login/methods", ""); !strings.Contains(rec.Body.String(), `"telegram":true`) {
		t.Fatalf("methods: %s", rec.Body.String())
	}
	tg := func(result string) *httptest.ResponseRecorder {
		t.Helper()
		return f.public(http.MethodPost, "/api/v1/auth/login/telegram", `{"result": "`+result+`"}`)
	}
	// The link's own signature is spent.
	if rec := tg(signTelegram(token, 777, "ann", f.deps.Now())); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a signature used twice: %d %s", rec.Code, rec.Body.String())
	}
	fresh := signTelegram(token, 777, "ann", f.deps.Now().Add(-time.Minute))
	if rec := tg(fresh); rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Set-Cookie"), auth.CookieName+"=") {
		t.Fatalf("login: %d %s", rec.Code, rec.Body.String())
	}
	for name, result := range map[string]string{
		"another bot's":    signTelegram("999:other", 777, "ann", f.deps.Now().Add(-2*time.Minute)),
		"an old one":       signTelegram(token, 777, "ann", f.deps.Now().Add(-time.Hour)),
		"nobody's account": signTelegram(token, 778, "bob", f.deps.Now().Add(-3*time.Minute)),
		"not base64":       "!!!",
	} {
		if rec := tg(result); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
}
