package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"mikan/internal/panel/auth"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

// keyHarness is a harness logged in, with a "read" key and a "full" key made the way an
// admin makes them: from the session, with the password.
type keyHarness struct {
	*harness
	api        string
	csrf       map[string]string
	read, full string
}

func newKeyHarness(t *testing.T) *keyHarness {
	t.Helper()
	h := newHarness(t)
	ctx := context.Background()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	set := settings.New(h.st.Q)
	for k, v := range map[string]any{settings.KeyPublicHost: "203.0.113.10", settings.KeyPanelPort: 21355} {
		if err := settings.Set(ctx, set, k, v); err != nil {
			t.Fatal(err)
		}
	}
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	k := &keyHarness{harness: h, api: "/" + adminPath + "/api/v1", csrf: map[string]string{"X-CSRF-Token": h.csrf}}
	mk := func(scope string) string {
		resp, body := h.do(http.MethodPost, k.api+"/api-keys", map[string]any{"name": scope, "scope": scope, "password": password}, k.csrf)
		var c struct {
			Key string `json:"key"`
		}
		if resp.StatusCode != http.StatusCreated || json.Unmarshal(body, &c) != nil {
			t.Fatalf("make %s key: %d %s", scope, resp.StatusCode, body)
		}
		return c.Key
	}
	k.read, k.full = mk("read"), mk("full")
	return k
}

// asKey sends a request with the key and no cookie.
func (k *keyHarness) asKey(key, method, path string, body any) (*http.Response, []byte) {
	k.t.Helper()
	jar := k.client.Jar
	k.client.Jar = nil
	defer func() { k.client.Jar = jar }()
	return k.do(method, k.api+path, body, map[string]string{"Authorization": "Bearer " + key})
}

// A leaked "full" key must not turn the panel against its owner: where clients are sent,
// who the bot talks to, the nodes' keys, the way out, the updates. These are the admin's
// session only; the same field of a request a key may otherwise make is refused too.
func TestFullKeyCannotTakeOverThePanel(t *testing.T) {
	k := newKeyHarness(t)
	ctx := context.Background()
	ins, _ := k.st.Q.ListInbounds(ctx)
	for name, c := range map[string]struct {
		method, path string
		body         any
	}{
		"update-telegram":    {http.MethodPatch, "/telegram", map[string]any{"token": "1:x"}},
		"telegram-broadcast": {http.MethodPost, "/telegram/broadcast", map[string]any{"text": "hi"}},
		"create-node":        {http.MethodPost, "/nodes", map[string]any{"name": "N", "host": "198.51.100.1"}},
		"update-node":        {http.MethodPatch, "/nodes/1", map[string]any{"name": "N"}},
		"rekey-node":         {http.MethodPost, "/nodes/1/key", nil},
		"request-update":     {http.MethodPost, "/updates/request", map[string]any{}},
		"update-updates":     {http.MethodPatch, "/updates", map[string]any{"auto": true}},
		"reset-admin-path":   {http.MethodPost, "/settings/reset-admin-path", nil},
		"warp-register":      {http.MethodPost, "/nodes/1/warp/register", map[string]any{}},
		"warp-import":        {http.MethodPost, "/nodes/1/warp/import", map[string]any{"config": "x"}},
		"cascade":            {http.MethodPatch, "/nodes/1/cascade", map[string]any{"outbound": "direct"}},
		// A code field is a script an addon runs.
		"update-addon": {http.MethodPatch, "/addons/telegram", map[string]any{"settings": map[string]any{"script": "print(1)"}}},
		// Code nothing signed vouches for, run on the server.
		"install-external-addon": {http.MethodPost, "/marketplace/external", map[string]any{"id": "x", "kind": "generic", "mode": "client", "permissions": []string{"users:write"}, "confirm": true}},
		// What a source may offer to run, and which sources are read.
		"add-addon-source":     {http.MethodPost, "/addon-sources", map[string]any{"url": "github.com/attacker/addons"}},
		"refresh-addon-source": {http.MethodPost, "/addon-sources/1/refresh", nil},
		"delete-addon-source":  {http.MethodDelete, "/addon-sources/1", nil},
		"install-marketplace":  {http.MethodPost, "/marketplace/x/install", map[string]any{"permissions": []string{}}},
		// The subscription page every subscriber opens: its links, text, CSS and images.
		"update-sub-page":       {http.MethodPut, "/sub-page", map[string]any{"config": map[string]any{}}},
		"put-sub-page-image":    {http.MethodPut, "/sub-page/images/logo", map[string]any{"data": "iVBORw0KGgo="}},
		"delete-sub-page-image": {http.MethodDelete, "/sub-page/images/logo", nil},
		"create-sub-doc":        {http.MethodPost, "/sub-docs", map[string]any{"title": "Pay here", "emoji": "", "body": "", "platform": "", "published": true}},
		"update-sub-doc":        {http.MethodPut, "/sub-docs/1", map[string]any{"title": "Pay here", "emoji": "", "body": "", "platform": "", "published": true}},
		"delete-sub-doc":        {http.MethodDelete, "/sub-docs/1", nil},
		"order-sub-docs":        {http.MethodPut, "/sub-docs/order", map[string]any{"ids": []int64{1}}},
	} {
		resp, body := k.asKey(k.full, c.method, c.path, c.body)
		if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "session_only") {
			t.Errorf("%s with a full key: %d %s, want 403 session_only", name, resp.StatusCode, body)
		}
	}
	// Fields of endpoints a key may use: the address of the panel, where clients connect,
	// the rules and the support link are the session's; the rest is not.
	for field, body := range map[string]map[string]any{
		"public_host": {"public_host": "attacker.example"}, "domain": {"domain": "attacker.example"}, "sub_port": {"sub_port": 8443},
		"sub_rules": {"sub_rules": "DOMAIN,evil.example,DIRECT"}, "support_url": {"support_url": "https://evil.example"},
		// What every subscriber's app shows.
		"sub_announce": {"sub_announce": "Pay here"}, "sub_announce_url": {"sub_announce_url": "https://evil.example"},
		"app_branding": {"app_branding": true}, "brand_accent": {"brand_accent": "#000000"}, "brand_logo_url": {"brand_logo_url": "https://evil.example/l.png"},
		// Where every client's traffic goes.
		"sub_routing": {"sub_routing": "blocked"}, "sub_routes": {"sub_routes": map[string]any{"services": map[string]any{"youtube": "direct"}}},
		"sub_template": {"sub_template": "rules: [MATCH,DIRECT]\n"},
		// Which apps get a subscription at all.
		"device_require_hwid": {"device_require_hwid": true}, "device_binding": {"device_binding": true},
		// Ports the firewall does not open.
		"any_port": {"any_port": true}, "wide_sni_scan": {"wide_sni_scan": true},
	} {
		resp, out := k.asKey(k.full, http.MethodPatch, "/settings", body)
		if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(out), "session_only") || !strings.Contains(string(out), field) {
			t.Errorf("settings.%s with a full key: %d %s", field, resp.StatusCode, out)
		}
	}
	if host, _, _ := settings.Get[string](ctx, settings.New(k.st.Q), settings.KeyPublicHost); host != "203.0.113.10" {
		t.Fatalf("a refused request changed the public host: %q", host)
	}
	if resp, body := k.asKey(k.full, http.MethodPatch, "/settings", map[string]any{"brand": "Keyed", "auto_port": true}); resp.StatusCode != http.StatusOK {
		t.Fatalf("a full key still changes what is harmless: %d %s", resp.StatusCode, body)
	}
	resp, out := k.asKey(k.full, http.MethodPatch, "/inbounds/"+idOf(ins[0].ID), map[string]any{"client": map[string]any{"server": "attacker.example", "port": 443, "sni": ""}})
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(out), "session_only") {
		t.Fatalf("inbound client endpoint with a full key: %d %s", resp.StatusCode, out)
	}
	if after, _ := k.st.Q.GetInbound(ctx, ins[0].ID); after != ins[0] {
		t.Fatal("a refused request changed the inbound")
	}
	if resp, body := k.asKey(k.full, http.MethodPatch, "/inbounds/"+idOf(ins[0].ID), map[string]any{"display_name": "Renamed"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("the rest of the inbound is open to a key: %d %s", resp.StatusCode, body)
	}
	// The session still does all of it.
	if resp, body := k.do(http.MethodPatch, k.api+"/settings", map[string]any{"sub_rules": "DOMAIN,example.org,DIRECT"}, k.csrf); resp.StatusCode != http.StatusOK {
		t.Fatalf("session: %d %s", resp.StatusCode, body)
	}
}

func idOf(id int64) string { return strconv.FormatInt(id, 10) }

// A "read" key reads, it does not take: subscription links (the users' credentials) and
// the secret addresses stay out of its answers. A full key and the session still see them.
func TestReadKeyDoesNotGetSecrets(t *testing.T) {
	k := newKeyHarness(t)
	ctx := context.Background()
	tariffs, _ := k.st.Q.ListTariffs(ctx)
	clock := func() time.Time { return k.now }
	u, err := domain.NewUsers(k.st, domain.NewPool(k.st, clock), noChanges{}, clock).Create(ctx, domain.CreateInput{Name: "a", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		path        string
		secret      string
		secretField string
	}{
		"users":    {"/users", u.SubToken, "sub_url"},
		"one user": {"/users/" + idOf(u.ID), u.SubToken, "sub_url"},
		"settings": {"/settings", adminPath, "admin_url"},
	} {
		resp, body := k.asKey(k.read, http.MethodGet, c.path, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d %s", name, resp.StatusCode, body)
		}
		if strings.Contains(string(body), c.secret) || strings.Contains(string(body), subPath) {
			t.Errorf("%s: a read key sees the secret in %s", name, body)
		}
		for _, who := range []struct {
			name string
			key  string
		}{{"full key", k.full}, {"session", ""}} {
			var resp *http.Response
			var body []byte
			if who.key != "" {
				resp, body = k.asKey(who.key, http.MethodGet, c.path, nil)
			} else {
				resp, body = k.do(http.MethodGet, k.api+c.path, nil, nil)
			}
			if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), c.secret) {
				t.Errorf("%s as %s: %d, the %s is missing: %s", name, who.name, resp.StatusCode, c.secretField, body)
			}
		}
	}

	// An own Clash profile may hold the admin's own proxies and secrets.
	const tpl = "secret: controller-secret-1234\nrules: [MATCH,DIRECT]\n"
	if err := settings.Set(ctx, settings.New(k.st.Q), settings.KeyTemplate, tpl); err != nil {
		t.Fatal(err)
	}
	if _, body := k.asKey(k.read, http.MethodGet, "/settings", nil); strings.Contains(string(body), "controller-secret-1234") {
		t.Errorf("a read key sees the own profile: %s", body)
	}
	if _, body := k.asKey(k.full, http.MethodGet, "/settings", nil); !strings.Contains(string(body), "controller-secret-1234") {
		t.Errorf("a full key misses the own profile: %s", body)
	}
}

// Making a key needs the password (and the 2FA code), not just a session: a stolen cookie
// does not leave a key behind. Wrong answers are counted like failed logins.
func TestMakingAKeyAsksForThePassword(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	path := "/" + adminPath + "/api/v1/api-keys"
	mint := func(body map[string]any) (*http.Response, []byte) { return h.do(http.MethodPost, path, body, csrf) }
	keys := func() int64 {
		n, err := h.st.Q.CountAPIKeys(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}

	if resp, _ := mint(map[string]any{"name": "x", "scope": "full"}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("no password: %d", resp.StatusCode)
	}
	if resp, body := mint(map[string]any{"name": "x", "scope": "full", "password": "not-the-password"}); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "wrong_password") {
		t.Fatalf("wrong password: %d %s", resp.StatusCode, body)
	}
	if keys() != 0 {
		t.Fatal("a key was made without the password")
	}
	if resp, body := mint(map[string]any{"name": "x", "scope": "full", "password": password}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("right password: %d %s", resp.StatusCode, body)
	}

	// With 2FA on, the code is needed too.
	key, _ := auth.NewTOTPKey("admin")
	if err := h.st.Q.SetAdminTOTP(ctx, db.SetAdminTOTPParams{TotpSecret: sql.NullString{String: key.Secret(), Valid: true}, ID: 1}); err != nil {
		t.Fatal(err)
	}
	if resp, body := mint(map[string]any{"name": "y", "scope": "full", "password": password}); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "invalid_totp") {
		t.Fatalf("no code: %d %s", resp.StatusCode, body)
	}
	if resp, _ := mint(map[string]any{"name": "y", "scope": "full", "password": password, "totp": "000000"}); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("wrong code: %d", resp.StatusCode)
	}
	code, _ := totp.GenerateCode(key.Secret(), h.now)
	if resp, body := mint(map[string]any{"name": "y", "scope": "full", "password": password, "totp": code}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("right code: %d %s", resp.StatusCode, body)
	}
	if keys() != 2 {
		t.Fatalf("keys: %d, want 2", keys())
	}

	// The password cannot be guessed through the session: the attempts run out.
	var last int
	for range 40 {
		resp, _ := mint(map[string]any{"name": "z", "scope": "read", "password": "guess"})
		if last = resp.StatusCode; last == http.StatusTooManyRequests {
			break
		}
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("guessing the password through a session was never stopped (last %d)", last)
	}
}

// Starting 2FA needs the password too: a stolen session must not lock the owner out by
// turning it on for itself.
func TestStartingTOTPAsksForThePassword(t *testing.T) {
	h := newHarness(t)
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	path := "/" + adminPath + "/api/v1/auth/totp/setup"
	if resp, _ := h.do(http.MethodPost, path, map[string]any{}, csrf); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("no password: %d", resp.StatusCode)
	}
	if resp, body := h.do(http.MethodPost, path, map[string]any{"password": "wrong"}, csrf); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "wrong_password") {
		t.Fatalf("wrong password: %d %s", resp.StatusCode, body)
	}
	resp, body := h.do(http.MethodPost, path, map[string]any{"password": password}, csrf)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"secret"`) {
		t.Fatalf("right password: %d %s", resp.StatusCode, body)
	}
}

// A new password ends the other sessions and, unless the admin says otherwise, the keys:
// a key made from a hijacked session would otherwise outlive the reaction to it.
func TestPasswordChangeRevokesKeys(t *testing.T) {
	for _, keep := range []bool{false, true} {
		k := newKeyHarness(t)
		body := map[string]any{"current": password, "new": "another-long-password"}
		if keep {
			body["revoke_keys"] = false
		}
		if resp, out := k.do(http.MethodPost, k.api+"/auth/password", body, k.csrf); resp.StatusCode != http.StatusNoContent {
			t.Fatalf("change password: %d %s", resp.StatusCode, out)
		}
		resp, _ := k.asKey(k.full, http.MethodGet, "/users", nil)
		if keep && resp.StatusCode != http.StatusOK {
			t.Errorf("kept keys: the key stopped working: %d", resp.StatusCode)
		}
		if !keep && resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("revoked keys: the key still works: %d", resp.StatusCode)
		}
		if n, _ := k.st.Q.CountAPIKeys(context.Background()); (n != 0) != keep {
			t.Errorf("keep=%v: %d keys left", keep, n)
		}
	}
}

// Parallel logins are counted before the password is hashed: of any number sent at once
// only the allowance (10 per address) gets to try, the rest are refused outright.
func TestParallelLoginsShareTheAllowance(t *testing.T) {
	h := newHarness(t)
	const n = 60
	codes := make(chan int, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := map[string]string{"username": "admin", "password": "wrong-password-x"}
			resp, _ := h.do(http.MethodPost, "/"+adminPath+"/api/v1/auth/login", body, nil)
			codes <- resp.StatusCode
		}()
	}
	wg.Wait()
	close(codes)
	tried, refused := 0, 0
	for c := range codes {
		switch c {
		case http.StatusUnauthorized:
			tried++
		case http.StatusTooManyRequests:
			refused++
		default:
			t.Fatalf("unexpected status %d", c)
		}
	}
	if tried != 10 || refused != n-10 {
		t.Fatalf("%d attempts were tried and %d refused, want 10 and %d", tried, refused, n-10)
	}
}

// One recovery code is one login, also when two logins use it at the same moment.
func TestRecoveryCodeIsSpentOnce(t *testing.T) {
	h := newHarness(t)
	key, _ := auth.NewTOTPKey("admin")
	plain, stored := auth.NewRecoveryCodes(2)
	if err := h.st.Q.SetAdminTOTP(context.Background(), db.SetAdminTOTPParams{
		TotpSecret: sql.NullString{String: key.Secret(), Valid: true}, RecoveryCodes: sql.NullString{String: stored, Valid: true}, ID: 1,
	}); err != nil {
		t.Fatal(err)
	}
	const n = 8
	codes := make(chan int, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			jar, _ := cookiejar.New(nil)
			cl := *h.client
			cl.Jar = jar
			raw, _ := json.Marshal(map[string]string{"username": "admin", "password": password, "totp": plain[0]})
			req, _ := http.NewRequest(http.MethodPost, h.ts.URL+"/"+adminPath+"/api/v1/auth/login", strings.NewReader(string(raw)))
			req.Header.Set("Content-Type", "application/json")
			resp, err := cl.Do(req)
			if err != nil {
				t.Error(err)
				codes <- 0
				return
			}
			resp.Body.Close()
			codes <- resp.StatusCode
		}()
	}
	wg.Wait()
	close(codes)
	ok := 0
	for c := range codes {
		if c == http.StatusOK {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("%d logins got in with one recovery code, want 1", ok)
	}
}

// A login with a name nobody has is not written to the audit log as typed: it is often a
// password pasted into the wrong field. A real admin's name is kept.
func TestFailedLoginAuditDoesNotKeepTheTypedName(t *testing.T) {
	h := newHarness(t)
	secret := "my-secret-password-pasted-here"
	if resp, _ := h.do(http.MethodPost, "/"+adminPath+"/api/v1/auth/login", map[string]string{"username": secret, "password": "x"}, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unknown name: %d", resp.StatusCode)
	}
	if resp, _ := h.do(http.MethodPost, "/"+adminPath+"/api/v1/auth/login", map[string]string{"username": "admin", "password": "x"}, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", resp.StatusCode)
	}
	rows, err := h.st.Q.ListAudit(context.Background(), db.ListAuditParams{BeforeID: 1 << 62, Lim: 10})
	if err != nil {
		t.Fatal(err)
	}
	var targets []string
	for _, r := range rows {
		if r.Action == "auth.login_failed" {
			targets = append(targets, r.TargetID.String)
		}
	}
	if len(targets) != 2 {
		t.Fatalf("failed logins in the audit log: %v", targets)
	}
	real, hashed := 0, 0
	for _, id := range targets {
		if strings.Contains(id, "secret") {
			t.Fatalf("the audit log kept the typed name: %q", id)
		}
		if id == "admin" {
			real++
		}
		if strings.HasPrefix(id, "unknown:") && len(id) == len("unknown:")+8 {
			hashed++
		}
	}
	if real != 1 || hashed != 1 {
		t.Fatalf("targets: %v, want the real admin's name once and unknown:<8 hex> once", targets)
	}
}
