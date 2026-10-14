package app

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mikan/internal/panel/addons"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
)

// The marketplace of protocol 2: the admin approves exactly the permissions shown, the grant
// is written before the host is asked, an update asking for more is marked, and removing
// takes the grant back at once. A server whose host predates protocol 2 is refused.
func TestMarketplaceGrants(t *testing.T) {
	digest := "sha256:" + strings.Repeat("b", 64)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	entry := addons.Entry{ID: "telegram", Name: addons.Text{"en": "Bot"}, Version: "1.0.0", Protocol: 2, Kind: addons.KindBot,
		Permissions: []string{"kv", "bot"}, Image: "ghcr.io/getmikan/telegram", Digest: digest}
	var index atomic.Value
	publish := func(entries ...addons.Entry) {
		data, _ := json.Marshal(addons.Catalog{Version: 1, Adapters: entries})
		index.Store(data)
	}
	publish(entry, addons.Entry{ID: "fake", Version: "1.0.0", Protocol: 1, Image: "ghcr.io/getmikan/adapter-fake", Digest: digest})
	catalog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data := index.Load().([]byte)
		switch r.URL.Path {
		case "/index.json":
			_, _ = w.Write(data)
		case "/index.json.sig":
			_, _ = io.WriteString(w, base64.StdEncoding.EncodeToString(ed25519.Sign(priv, data)))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(catalog.Close)

	dir := t.TempDir()
	h := newHarness(t, func(o *Options) { o.DataDir, o.AddonsCatalog = dir, catalog.URL+"/index.json" })
	h.p.Addons.SetKey(pub)
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	api := "/" + adminPath + "/api/v1/marketplace"
	type view struct {
		Catalog []struct {
			ID          string   `json:"id"`
			Permissions []string `json:"permissions"`
			Installed   bool     `json:"installed"`
			Wider       bool     `json:"wider"`
		} `json:"catalog"`
		Installed []struct {
			ID      string   `json:"id"`
			Granted []string `json:"granted"`
		} `json:"installed"`
		HostReady bool `json:"host_ready"`
	}
	read := func(resp *http.Response, body []byte) view {
		t.Helper()
		var v view
		if resp.StatusCode/100 != 2 || json.Unmarshal(body, &v) != nil {
			t.Fatalf("marketplace: %d %s", resp.StatusCode, body)
		}
		return v
	}
	host := func(st addons.State) {
		t.Helper()
		data, _ := json.Marshal(st)
		if err := os.MkdirAll(filepath.Join(dir, "addons"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "addons", "state.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	approve := func(perms ...string) map[string]any {
		return map[string]any{"permissions": append([]string{}, perms...)}
	}
	grant := func() (string, bool) {
		g, err := h.st.Q.GetAddonGrant(context.Background(), "telegram")
		if errors.Is(err, sql.ErrNoRows) {
			return "", false
		}
		if err != nil {
			t.Fatal(err)
		}
		return g.Permissions, true
	}

	// Only protocol 2 shows here; the payment adapter stays with payments.
	v := read(h.do(http.MethodGet, api, nil, nil))
	if len(v.Catalog) != 1 || v.Catalog[0].ID != "telegram" || v.HostReady {
		t.Fatalf("catalog: %+v", v)
	}
	if resp, body := h.do(http.MethodPost, api+"/telegram/install", approve("bot", "kv"), csrf); resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "host_outdated") {
		t.Fatalf("a host before protocol 2: %d %s", resp.StatusCode, body)
	}
	host(addons.State{Adapters: map[string]addons.Installed{}, HostVersion: "0.5.1.0"})
	if resp, body := h.do(http.MethodPost, api+"/fake/install", approve(), csrf); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a payment adapter through this marketplace: %d %s", resp.StatusCode, body)
	}
	for _, perms := range [][]string{{"bot"}, {"bot", "kv", "users:write"}, nil} {
		if resp, body := h.do(http.MethodPost, api+"/telegram/install", approve(perms...), csrf); resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "permissions_changed") {
			t.Fatalf("approving %v: %d %s", perms, resp.StatusCode, body)
		}
	}
	if _, ok := grant(); ok {
		t.Fatal("a refused approval left a grant")
	}
	read(h.do(http.MethodPost, api+"/telegram/install", approve("kv", "bot"), csrf))
	if g, ok := grant(); !ok || g != `["bot","kv"]` {
		t.Fatalf("grant: %q %v", g, ok)
	}
	if q := h.p.Addons.Pending(); len(q) != 1 || q[0].Action != "install" || q[0].ID != "telegram" {
		t.Fatalf("the host is not asked: %+v", q)
	}

	// The host ran it; the next build asks for more.
	if err := os.RemoveAll(filepath.Join(dir, "addons", "requests")); err != nil {
		t.Fatal(err)
	}
	host(addons.State{HostVersion: "0.5.1.0", Adapters: map[string]addons.Installed{"telegram": {Version: "1.0.0", Digest: digest,
		Protocol: 2, Kind: addons.KindBot, Status: "running"}}})
	wider := entry
	wider.Version, wider.Digest, wider.Permissions = "1.1.0", "sha256:"+strings.Repeat("c", 64), []string{"bot", "kv", "users:write"}
	publish(wider)
	h.now = h.now.Add(11 * time.Minute) // past the cached catalog
	v = read(h.do(http.MethodGet, api, nil, nil))
	if len(v.Installed) != 1 || strings.Join(v.Installed[0].Granted, ",") != "bot,kv" {
		t.Fatalf("installed: %+v", v.Installed)
	}
	if len(v.Catalog) != 1 || !v.Catalog[0].Wider || !v.Catalog[0].Installed {
		t.Fatalf("an update asking for more is not marked: %+v", v.Catalog)
	}

	// A grant key of the addon is its own: removing the addon takes it too, so one installed
	// later under the id does not get the old one's results.
	ctx := context.Background()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	tariffs, _ := h.st.Q.ListTariffs(ctx)
	clock := func() time.Time { return h.now }
	u, err := domain.NewUsers(h.st, domain.NewPool(h.st, clock), noChanges{}, clock).Create(ctx, domain.CreateInput{Name: "a", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"telegram", "neighbour"} {
		if n, err := h.st.Q.ClaimAddonGrantKey(ctx, db.ClaimAddonGrantKeyParams{AddonID: id, Key: "k", UserID: u.ID, CreatedAt: 1}); err != nil || n != 1 {
			t.Fatalf("claim: %d %v", n, err)
		}
	}
	if resp, body := h.do(http.MethodPost, api+"/telegram/remove", nil, csrf); resp.StatusCode/100 != 2 {
		t.Fatalf("remove: %d %s", resp.StatusCode, body)
	}
	if _, ok := grant(); ok {
		t.Fatal("the grant outlived the removal")
	}
	if _, err := h.st.Q.GetAddonGrantKey(ctx, db.GetAddonGrantKeyParams{AddonID: "telegram", Key: "k"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("the grant key outlived the removal: %v", err)
	}
	if _, err := h.st.Q.GetAddonGrantKey(ctx, db.GetAddonGrantKeyParams{AddonID: "neighbour", Key: "k"}); err != nil {
		t.Fatalf("another addon's key went with it: %v", err)
	}
}
