package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

// fakeMarzban answers like Marzban (or PasarGuard with ISO dates and ids) to an admin
// with the password "pw".
func fakeMarzban(t *testing.T, users []map[string]any) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/admin/token":
			_ = r.ParseForm()
			if r.PostForm.Get("username") != "admin" || r.PostForm.Get("password") != "pw" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "token_type": "bearer"})
		case "/api/users":
			if r.Header.Get("Authorization") != "Bearer tok" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			page := users
			if r.URL.Query().Get("offset") != "0" {
				page = nil
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"users": page, "total": len(users)})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func fakeRemnawave(t *testing.T, users []map[string]any) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/users/stream" || r.Header.Get("Authorization") != "Bearer rw-token" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"users": users, "nextCursor": nil, "hasMore": false}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

type countList struct {
	Names []string `json:"names"`
	Count int      `json:"count"`
}

type importReport struct {
	Created int       `json:"created"`
	Links   int       `json:"links"`
	Skipped countList `json:"skipped"`
	NoLink  countList `json:"no_link"`
	Failed  countList `json:"failed"`
}

type jobState struct {
	Mode    string        `json:"mode"`
	State   string        `json:"state"`
	Error   string        `json:"error"`
	Report  *importReport `json:"report"`
	Preview *struct {
		Total  int       `json:"total"`
		New    int       `json:"new"`
		OnHold int       `json:"on_hold"`
		Taken  countList `json:"taken"`
	} `json:"preview"`
}

// waitJob polls the import's state until it ends.
func waitJob(t *testing.T, h *harness, api string, csrf map[string]string) jobState {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		_, body := h.do(http.MethodGet, api+"/import/status", nil, csrf)
		var st jobState
		if err := json.Unmarshal(body, &st); err != nil {
			t.Fatal(err)
		}
		if st.State == "done" || st.State == "failed" {
			return st
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the job did not end")
	return jobState{}
}

// runPreview starts a preview and waits for it.
func runPreview(t *testing.T, h *harness, api string, csrf map[string]string, src map[string]any) jobState {
	t.Helper()
	if resp, body := h.do(http.MethodPost, api+"/import/preview", src, csrf); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("preview: %d %s", resp.StatusCode, body)
	}
	return waitJob(t, h, api, csrf)
}

// runImport starts an import and waits for its report: it runs in the background.
func runImport(t *testing.T, h *harness, api string, csrf map[string]string, run map[string]any) importReport {
	t.Helper()
	resp, body := h.do(http.MethodPost, api+"/import", run, csrf)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("start: %d %s", resp.StatusCode, body)
	}
	st := waitJob(t, h, api, csrf)
	if st.State != "done" || st.Report == nil {
		t.Fatalf("the import failed: %+v", st)
	}
	return *st.Report
}

func TestImportFromMarzbanOverHTTP(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]any{settings.KeyPublicHost: "203.0.113.10", settings.KeyPanelPort: 21355} {
		if err := settings.Set(ctx, settings.New(h.st.Q), k, v); err != nil {
			t.Fatal(err)
		}
	}
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	api := "/" + adminPath + "/api/v1"
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	tariffs, _ := h.st.Q.ListTariffs(ctx)

	old := fakeMarzban(t, []map[string]any{
		{"username": "ivan_petrov", "status": "active", "data_limit": 107374182400, "used_traffic": 5368709120, "lifetime_used_traffic": 21474836480, "expire": 1893456000, "note": "VIP"},
		{"username": "olga", "status": "disabled", "data_limit": nil, "used_traffic": 0, "lifetime_used_traffic": 0, "expire": nil},
		{"username": "pause", "status": "on_hold", "data_limit": 0, "used_traffic": 0, "lifetime_used_traffic": 0, "expire": nil, "on_hold_expire_duration": 2592000},
	})
	src := map[string]any{"kind": "marzban", "url": old.URL, "username": "admin", "password": "pw"}

	bad := map[string]any{"kind": "marzban", "url": old.URL, "username": "admin", "password": "wrong"}
	if st := runPreview(t, h, api, csrf, bad); st.State != "failed" || st.Error != "import_auth" {
		t.Fatalf("a wrong password: %+v", st)
	}
	if st := runPreview(t, h, api, csrf, src); st.Preview == nil || st.Preview.Total != 3 || st.Preview.New != 3 || st.Preview.OnHold != 1 {
		t.Fatalf("preview: %+v", st)
	}

	run := map[string]any{"kind": "marzban", "url": old.URL, "username": "admin", "password": "pw", "tariff_id": tariffs[1].ID}
	r := runImport(t, h, api, csrf, run)
	if r.Created != 3 || r.Links != 3 {
		t.Fatalf("import: %+v", r)
	}
	byName := map[string]int64{}
	list, _ := h.st.Q.ListUsers(ctx)
	for _, u := range list {
		byName[u.Name] = u.ID
		// An imported user says where it came from, on the list and in the card.
		if u.Source != domain.UserFromImport {
			t.Fatalf("user %s came from %q, want an import", u.Name, u.Source)
		}
	}
	ivan, _ := h.st.Q.GetUser(ctx, byName["ivan_petrov"])
	if !ivan.TrafficLimit.Valid || ivan.TrafficLimit.Int64 != 107374182400 || ivan.UsedDown != 5368709120 || ivan.TotalDown != 21474836480 ||
		!ivan.ExpiresAt.Valid || ivan.ExpiresAt.Int64 != 1893456000 || ivan.Note != "VIP" || ivan.TariffID.Int64 != tariffs[1].ID {
		t.Fatalf("ivan: %+v", ivan)
	}
	olga, _ := h.st.Q.GetUser(ctx, byName["olga"])
	if olga.Status != "disabled" || olga.TrafficLimit.Valid || olga.ExpiresAt.Valid {
		t.Fatalf("olga: %+v", olga)
	}
	pause, _ := h.st.Q.GetUser(ctx, byName["pause"])
	if !pause.ExpiresAt.Valid || pause.ExpiresAt.Int64 != h.now.Unix()+2592000 {
		t.Fatalf("an on-hold term starts at the import: %+v", pause)
	}
	// Again: everyone is there already, nobody is merged.
	if r := runImport(t, h, api, csrf, run); r.Created != 0 || r.Skipped.Count != 3 {
		t.Fatalf("second import: %+v", r)
	}

	// Old links: off until the path and the secret are set.
	const secret = "s3cr3t-key-from-jwt-table"
	const ivanToken = "aXZhbl9wZXRyb3YsMTc1OTQwMDAwMAUGNuNGBJks" // Marzban's own code, this secret
	sub := func(path string) int {
		t.Helper()
		resp, _ := h.do(http.MethodGet, path, nil, map[string]string{"User-Agent": "mihomo/1.19.32"})
		return resp.StatusCode
	}
	if sub("/sub/"+ivanToken) != http.StatusNotFound {
		t.Fatal("an old link works without being turned on")
	}
	resp, body := h.do(http.MethodPatch, api+"/import/legacy", map[string]any{"path": adminPath}, csrf)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "legacy_path_invalid") {
		t.Fatalf("the admin path as the old path: %d %s", resp.StatusCode, body)
	}
	resp, body = h.do(http.MethodPatch, api+"/import/legacy", map[string]any{"path": "/sub/", "kind": "marzban", "secret": secret}, csrf)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"secret_set":true`) || strings.Contains(string(body), secret) {
		t.Fatalf("legacy settings: %d %s", resp.StatusCode, body)
	}
	if _, err := h.p.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if got := sub("/sub/" + ivanToken); got != http.StatusOK {
		t.Fatalf("ivan's old link: %d", got)
	}
	for _, forged := range []string{
		"/sub/aXZhbl9wZXRyb3YsMTc1OTQwMDAwMAAAAAAAAAAA", // ivan's payload, a guessed signature
		"/sub/name:ivan_petrov",                         // the key the token is filed under
		"/sub/" + ivanToken + "x",
	} {
		if got := sub(forged); got != http.StatusNotFound {
			t.Errorf("%s: %d, a forgery passed", forged, got)
		}
	}
}

func TestImportFromRemnawaveKeepsShortUUIDLinks(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]any{settings.KeyPublicHost: "203.0.113.10", settings.KeyPanelPort: 21355} {
		if err := settings.Set(ctx, settings.New(h.st.Q), k, v); err != nil {
			t.Fatal(err)
		}
	}
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	api := "/" + adminPath + "/api/v1"
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	tariffs, _ := h.st.Q.ListTariffs(ctx)
	old := fakeRemnawave(t, []map[string]any{
		{"id": 1, "shortUuid": "Abc123Def456Ghi7", "username": "masha", "status": "ACTIVE", "trafficLimitBytes": 0, "expireAt": "2099-12-31T00:00:00.000Z",
			"telegramId": 777, "hwidDeviceLimit": 2, "userTraffic": map[string]any{"usedTrafficBytes": 100, "lifetimeUsedTrafficBytes": 1000}},
	})
	run := map[string]any{"kind": "remnawave", "url": old.URL, "token": "rw-token", "tariff_id": tariffs[1].ID}
	if r := runImport(t, h, api, csrf, run); r.Created != 1 || r.Links != 1 {
		t.Fatalf("import: %+v", r)
	}
	// Remnawave's links need no secret: the kind for signed links is left alone.
	if resp, body := h.do(http.MethodGet, api+"/import/legacy", nil, csrf); !strings.Contains(string(body), `"kind":""`) {
		t.Fatalf("a Remnawave import set the signed links' kind: %d %s", resp.StatusCode, body)
	}
	list, _ := h.st.Q.ListUsers(ctx)
	var masha = list[len(list)-1]
	if masha.Name != "masha" || masha.ExpiresAt.Valid || masha.TrafficLimit.Valid || !masha.DeviceLimit.Valid || masha.DeviceLimit.Int64 != 2 || masha.Contact != "tg:777" || masha.TotalDown != 1000 {
		t.Fatalf("masha: %+v", masha)
	}
	if resp, body := h.do(http.MethodPatch, api+"/import/legacy", map[string]any{"path": "api/sub"}, csrf); resp.StatusCode != http.StatusOK {
		t.Fatalf("legacy path: %d %s", resp.StatusCode, body)
	}
	if _, err := h.p.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]int{
		"/api/sub/Abc123Def456Ghi7":        http.StatusOK,
		"/api/sub/Abc123Def456Ghi7/mihomo": http.StatusOK,
		"/api/sub/Abc123Def456Ghi8":        http.StatusNotFound,
	} {
		resp, body := h.do(http.MethodGet, path, nil, map[string]string{"User-Agent": "Happ/3.4.1"})
		if resp.StatusCode != want {
			t.Errorf("%s: %d %.80s, want %d", path, resp.StatusCode, body, want)
		}
	}
	// The client type after the token picks the format, as in Remnawave.
	if _, body := h.do(http.MethodGet, "/api/sub/Abc123Def456Ghi7/mihomo", nil, map[string]string{"User-Agent": "Happ/3.4.1"}); !strings.Contains(string(body), "proxies") {
		t.Errorf("/mihomo did not give a Clash profile: %.120s", body)
	}
}

// 3x-ui (a version without CSRF) at its secret path: the clients of one subId become one
// user, a client only on vmess is reported and not made, and the subId opens the old link.
func TestImportFrom3xUIKeepsSubIDLinks(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]any{settings.KeyPublicHost: "203.0.113.10", settings.KeyPanelPort: 21355} {
		if err := settings.Set(ctx, settings.New(h.st.Q), k, v); err != nil {
			t.Fatal(err)
		}
	}
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	api := "/" + adminPath + "/api/v1"
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	inbounds, err := os.ReadFile("../panelimport/testdata/3xui-inbounds.json")
	if err != nil {
		t.Fatal(err)
	}
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/x7k/login":
			_ = r.ParseForm()
			if r.PostForm.Get("username") == "admin" && r.PostForm.Get("password") == "pw" {
				http.SetCookie(w, &http.Cookie{Name: "3x-ui", Value: "s", Path: "/"})
				_, _ = w.Write([]byte(`{"success":true}`))
				return
			}
			_, _ = w.Write([]byte(`{"success":false}`))
		case "/x7k/panel/api/inbounds/list":
			if c, err := r.Cookie("3x-ui"); err != nil || c.Value != "s" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write(inbounds)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(old.Close)
	tariffs, _ := h.st.Q.ListTariffs(ctx)
	run := map[string]any{"kind": "3xui", "url": old.URL + "/x7k", "username": "admin", "password": "pw", "tariff_id": tariffs[1].ID}
	r := runImport(t, h, api, csrf, run)
	// anna, boris and egor have a subId fit to be a secret; vera's is too short, dima has none.
	if r.Created != 5 || r.Links != 3 || r.Failed.Count != 1 || !strings.Contains(r.Failed.Names[0], "gleb") || r.NoLink.Count != 1 {
		t.Fatalf("import: %+v", r)
	}
	users, _ := h.st.Q.ListUsers(ctx)
	var anna db.User
	for _, u := range users {
		if u.Name == "anna" {
			anna = u
		}
	}
	if !anna.TrafficLimit.Valid || anna.TrafficLimit.Int64 != 20<<30 || anna.UsedDown != 2000 || !anna.DeviceLimit.Valid || anna.DeviceLimit.Int64 != 2 || anna.Contact != "tg:777" {
		t.Fatalf("anna: %+v", anna)
	}
	if resp, body := h.do(http.MethodPatch, api+"/import/legacy", map[string]any{"path": "xui/sub"}, csrf); resp.StatusCode != http.StatusOK {
		t.Fatalf("legacy path: %d %s", resp.StatusCode, body)
	}
	if _, err := h.p.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]int{
		"/xui/sub/anna0sub0long0id": http.StatusOK,
		"/xui/sub/anna0sub0long0ie": http.StatusNotFound,
	} {
		resp, body := h.do(http.MethodGet, path, nil, map[string]string{"User-Agent": "Happ/3.4.1"})
		if resp.StatusCode != want {
			t.Errorf("%s: %d %.80s, want %d", path, resp.StatusCode, body, want)
		}
	}
}

// PasarGuard: an API key instead of a login, ISO dates, and links signed with its own
// format (v3, with the user's id) open once the secret is set.
func TestImportFromPasarGuardKeepsSignedLinks(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]any{settings.KeyPublicHost: "203.0.113.10", settings.KeyPanelPort: 21355} {
		if err := settings.Set(ctx, settings.New(h.st.Q), k, v); err != nil {
			t.Fatal(err)
		}
	}
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	api := "/" + adminPath + "/api/v1"
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	tariffs, _ := h.st.Q.ListTariffs(ctx)
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "pg_key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"total": 1, "users": []map[string]any{
			{"id": 42, "username": "olga", "status": "active", "expire": "2030-01-01T00:00:00+00:00", "hwid_limit": 2},
		}})
	}))
	defer old.Close()
	run := map[string]any{"kind": "pasarguard", "url": old.URL, "token": "pg_key", "tariff_id": tariffs[1].ID}
	// One person with two keys (the name and the id): one user with old links, not two.
	if r := runImport(t, h, api, csrf, run); r.Created != 1 || r.Links != 1 {
		t.Fatalf("import: %+v", r)
	}
	// PasarGuard's own code made this token for user id 42 with this secret.
	const token = "djMsNDIsMTc1OTQwMDAwMA.fa-02Knno9F0qsz39WtypLtudEubWHfKFrYoyF8skR4"
	if resp, body := h.do(http.MethodPatch, api+"/import/legacy", map[string]any{"path": "sub", "secret": "s3cr3t-key-from-jwt-table"}, csrf); resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"kind":"pasarguard"`) || !strings.Contains(string(body), `"links":1`) {
		t.Fatalf("legacy: %d %s", resp.StatusCode, body)
	}
	if _, err := h.p.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if resp, _ := h.do(http.MethodGet, "/sub/"+token, nil, map[string]string{"User-Agent": "mihomo/1.19.32"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("olga's old link: %d", resp.StatusCode)
	}
	if resp, _ := h.do(http.MethodGet, "/sub/id:42", nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatal("the key a token is filed under opens a subscription")
	}
}

// Everything about imports is the session's: an API key, even a full one, gets 403.
func TestImportIsSessionOnly(t *testing.T) {
	k := newKeyHarness(t)
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/import/preview"}, {http.MethodPost, "/import"}, {http.MethodGet, "/import/status"}, {http.MethodDelete, "/import"},
		{http.MethodGet, "/import/legacy"}, {http.MethodPatch, "/import/legacy"},
	} {
		resp, body := k.asKey(k.full, c.method, c.path, map[string]any{})
		if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "session_only") {
			t.Errorf("%s %s with a full key: %d %s", c.method, c.path, resp.StatusCode, body)
		}
	}
}

// A Marzban import on top of PasarGuard's links is refused: one secret checks the signed
// links, and the other panel's would stop opening without a word.
func TestImportRefusesMixingSignedPanels(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	api := "/" + adminPath + "/api/v1"
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	tariffs, _ := h.st.Q.ListTariffs(ctx)
	users, _ := h.st.Q.ListUsers(ctx)
	if len(users) == 0 {
		u, err := domain.NewUsers(h.st, domain.NewPool(h.st, func() time.Time { return h.now }), noChanges{}, func() time.Time { return h.now }).Create(ctx, domain.CreateInput{Name: "x", TariffID: tariffs[1].ID})
		if err != nil {
			t.Fatal(err)
		}
		users = append(users, u)
	}
	if _, err := h.st.Q.AddLegacySubToken(ctx, db.AddLegacySubTokenParams{Token: "name:x", UserID: users[0].ID, Source: "pasarguard"}); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, settings.New(h.st.Q), settings.KeyLegacySubKind, "pasarguard"); err != nil {
		t.Fatal(err)
	}
	// The preview says so too, before the admin reads it and picks a plan.
	check := map[string]any{"kind": "marzban", "url": "https://old.example.com", "username": "a", "password": "b"}
	if resp, body := h.do(http.MethodPost, api+"/import/preview", check, csrf); resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "import_kind_conflict") {
		t.Fatalf("mixing, preview: %d %s", resp.StatusCode, body)
	}
	run := map[string]any{"kind": "marzban", "url": "https://old.example.com", "username": "a", "password": "b", "tariff_id": tariffs[1].ID}
	if resp, body := h.do(http.MethodPost, api+"/import", run, csrf); resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "import_kind_conflict") {
		t.Fatalf("mixing: %d %s", resp.StatusCode, body)
	}
}
