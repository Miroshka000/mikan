package app

import (
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mikan/internal/panel/addons"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/tgate"
)

// The marketplace end to end on the panel's side: the signed catalog, the request to the
// host, and an adapter's settings, with its secret never coming back.
func TestAddonsMarketplace(t *testing.T) {
	const secret = "sk_live_MARKETsecret0"
	digest := "sha256:" + strings.Repeat("b", 64)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	index, _ := json.Marshal(addons.Catalog{Version: 1, Adapters: []addons.Entry{
		{ID: "fake", Name: addons.Text{"en": "Fake"}, Version: "1.0.0", Protocol: 1, Image: "ghcr.io/getmikan/adapter-fake", Digest: digest}}})
	catalog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.json":
			_, _ = w.Write(index)
		case "/index.json.sig":
			_, _ = io.WriteString(w, base64.StdEncoding.EncodeToString(ed25519.Sign(priv, index)))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(catalog.Close)
	adapter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Settings map[string]any `json:"settings"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		switch {
		case r.Header.Get("Authorization") != "Bearer tok":
			w.WriteHeader(http.StatusUnauthorized)
		case r.URL.Path == "/v1/info":
			_, _ = io.WriteString(w, `{"id":"fake","protocol":1,"version":"1.0.0","name":{"en":"Fake"},"currencies":["RUB"],
				"settings":[{"key":"shop_id","type":"string","required":true},{"key":"secret_key","type":"string","secret":true,"required":true}]}`)
		case r.URL.Path == "/v1/check" && in.Settings["secret_key"] == secret:
			_, _ = io.WriteString(w, `{}`)
		case r.URL.Path == "/v1/invoices" && in.Settings["secret_key"] == secret:
			_, _ = io.WriteString(w, `{"external_id":"fk-1","pay_url":"https://pay.example/fk-1"}`)
		default:
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = io.WriteString(w, `{"code":"bad_credentials","message":"refused"}`)
		}
	}))
	t.Cleanup(adapter.Close)

	dir := t.TempDir()
	h := newHarness(t, func(o *Options) { o.DataDir, o.AddonsCatalog = dir, catalog.URL+"/index.json" })
	h.p.Addons.SetKey(pub)
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	api := "/" + adminPath + "/api/v1/addons"
	type view struct {
		Catalog []struct {
			ID        string `json:"id"`
			Installed bool   `json:"installed"`
		} `json:"catalog"`
		Installed []struct {
			ID         string `json:"id"`
			Enabled    bool   `json:"enabled"`
			Available  bool   `json:"available"`
			WebhookURL string `json:"webhook_url"`
			Settings   []struct {
				Key   string `json:"key"`
				Value any    `json:"value"`
				Set   bool   `json:"set"`
			} `json:"settings"`
		} `json:"installed"`
		Queue []struct {
			ID string `json:"id"`
		} `json:"queue"`
		Supported bool `json:"supported"`
	}
	read := func(resp *http.Response, body []byte) view {
		t.Helper()
		var v view
		if resp.StatusCode/100 != 2 || json.Unmarshal(body, &v) != nil {
			t.Fatalf("addons: %d %s", resp.StatusCode, body)
		}
		return v
	}
	if v := read(h.do(http.MethodGet, api, nil, nil)); len(v.Catalog) != 1 || v.Catalog[0].ID != "fake" || len(v.Installed) != 0 || !v.Supported {
		t.Fatalf("catalog: %+v", v)
	}
	if resp, body := h.do(http.MethodPost, api+"/ghost/install", nil, csrf); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("not in the catalog: %d %s", resp.StatusCode, body)
	}
	if v := read(h.do(http.MethodPost, api+"/fake/install", nil, csrf)); len(v.Queue) != 1 || v.Queue[0].ID != "fake" {
		t.Fatalf("install: %+v", v)
	}
	if wake, err := os.ReadFile(filepath.Join(dir, "update", "request")); err != nil || !strings.Contains(string(wake), `"do":"addons"`) {
		t.Fatalf("the host is not woken: %s %v", wake, err)
	}
	if resp, _ := h.do(http.MethodPost, api+"/fake/install", nil, csrf); resp.StatusCode != http.StatusConflict {
		t.Fatalf("a second request while one waits: %d", resp.StatusCode)
	}

	// The host runs it.
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.RemoveAll(filepath.Join(dir, "addons", "requests")))
	state, _ := json.Marshal(addons.State{Adapters: map[string]addons.Installed{"fake": {Version: "1.0.0", Digest: digest, Status: "running",
		Listen: strings.TrimPrefix(adapter.URL, "http://"), Token: "tok"}}})
	must(os.WriteFile(filepath.Join(dir, "addons", "state.json"), state, 0o600))
	if resp, _ := h.do(http.MethodPost, api+"/fake/install", nil, csrf); resp.StatusCode != http.StatusConflict {
		t.Fatalf("installing the same build again: %d", resp.StatusCode)
	}
	if resp, body := h.do(http.MethodPatch, api+"/fake", map[string]any{"enabled": true, "settings": map[string]any{"shop_id": "12", "secret_key": "wrong"}}, csrf); resp.StatusCode != http.StatusUnprocessableEntity ||
		!strings.Contains(string(body), "bad_credentials") {
		t.Fatalf("refused keys: %d %s", resp.StatusCode, body)
	}
	must(settings.Set(t.Context(), settings.New(h.st.Q), settings.KeyPublicHost, "203.0.113.10"))
	// The webhook address is the panel's: no address, no webhook, and a port is part of it.
	must(settings.Set(t.Context(), settings.New(h.st.Q), settings.KeyPanelPort, 21355))
	if resp, body := h.do(http.MethodPatch, "/"+adminPath+"/api/v1/payments/settings", map[string]any{"enabled": true}, csrf); resp.StatusCode != http.StatusOK {
		t.Fatalf("selling on: %d %s", resp.StatusCode, body)
	}
	resp, body := h.do(http.MethodPatch, api+"/fake", map[string]any{"enabled": true, "settings": map[string]any{"shop_id": "12", "secret_key": secret}}, csrf)
	v := read(resp, body)
	if len(v.Installed) != 1 || !v.Installed[0].Enabled || !v.Installed[0].Available || !strings.Contains(v.Installed[0].WebhookURL, "/pay/addon/fake/") {
		t.Fatalf("set up: %s", body)
	}
	for _, f := range v.Installed[0].Settings {
		if !f.Set || f.Key == "secret_key" && f.Value != nil || f.Key == "shop_id" && f.Value != "12" {
			t.Fatalf("setting %+v", f)
		}
	}
	if _, body := h.do(http.MethodGet, api, nil, nil); strings.Contains(string(body), secret) {
		t.Fatal("the secret came back")
	}
	var audit string
	must(h.st.DB.QueryRow("SELECT string_agg(action || ' ' || details, ';' ORDER BY id) FROM audit_log WHERE action LIKE 'addon.%'").Scan(&audit))
	if strings.Contains(audit, secret) || !strings.Contains(audit, "addon.settings") || !strings.Contains(audit, "secret_key") {
		t.Fatalf("audit: %s", audit)
	}
	// The Mini App offers it by its own name and opens its invoice.
	ctx := t.Context()
	must(domain.Seed(ctx, h.st, h.now))
	must(settings.Set(ctx, settings.New(h.st.Q), tgate.KeyToken, tgToken))
	ts, err := h.st.Q.ListTariffs(ctx)
	must(err)
	sale, err := h.st.Q.UpdateTariff(ctx, db.UpdateTariffParams{Name: "Месяц", TrafficLimit: ts[1].TrafficLimit, DurationDays: 30, DeviceLimit: ts[1].DeviceLimit,
		ResetStrategy: ts[1].ResetStrategy, PriceRub: sql.NullInt64{Int64: 19900, Valid: true}, OnSale: 1, ID: ts[1].ID})
	must(err)
	same := map[string]string{"Sec-Fetch-Site": "same-origin"}
	resp, body = h.do(http.MethodPost, "/"+subPath+"/tg/shop", map[string]any{"init_data": initData(tgToken, 555, h.now)}, same)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"addons":[{"provider":"addon:fake","name":"Fake"}]`) {
		t.Fatalf("Mini App shop: %d %s", resp.StatusCode, body)
	}
	resp, body = h.do(http.MethodPost, "/"+subPath+"/tg/pay", map[string]any{"init_data": initData(tgToken, 555, h.now), "tariff_id": sale.ID, "provider": "addon:fake"}, same)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "https://pay.example/fk-1") {
		t.Fatalf("Mini App pay: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.do(http.MethodPost, "/"+subPath+"/tg/pay", map[string]any{"init_data": initData(tgToken, 555, h.now), "tariff_id": sale.ID, "provider": "addon:ghost"}, same); resp.StatusCode != http.StatusConflict {
		t.Fatalf("an adapter that is not installed: %d %s", resp.StatusCode, body)
	}

	if resp, body := h.do(http.MethodPost, api+"/fake/remove", nil, csrf); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("remove: %d %s", resp.StatusCode, body)
	}
}
