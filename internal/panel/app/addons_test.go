package app

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
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
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/tgate"
	"mikan/internal/release"
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
		case "/index.json" + release.SigSuffix:
			_, _ = io.WriteString(w, release.SignIn(release.CatalogContext, index, priv))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(catalog.Close)
	var failWebInvoice atomic.Bool
	webInvoiceKeys := make(chan string, 10)
	adapter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Settings       map[string]any `json:"settings"`
			PaymentID      int64          `json:"payment_id"`
			IdempotencyKey string         `json:"idempotency_key"`
			ReturnURL      string         `json:"return_url"`
			WebhookURL     string         `json:"webhook_url"`
			ReceiptEmail   string         `json:"receipt_email"`
			ExternalID     string         `json:"external_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		switch {
		case r.Header.Get("Authorization") != "Bearer tok":
			w.WriteHeader(http.StatusUnauthorized)
		case r.URL.Path == "/v1/info":
			_, _ = io.WriteString(w, `{"id":"fake","protocol":1,"version":"1.0.0","name":{"en":"Fake"},"currencies":["RUB"],"capabilities":["receipt_email"],
				"settings":[{"key":"shop_id","type":"string","required":true},{"key":"secret_key","type":"string","secret":true,"required":true}]}`)
		case r.URL.Path == "/v1/check" && in.Settings["secret_key"] == secret:
			_, _ = io.WriteString(w, `{}`)
		case r.URL.Path == "/v1/invoices" && in.Settings["secret_key"] == secret && strings.HasSuffix(in.ReturnURL, "/shop"):
			switch in.ReceiptEmail {
			case "edge-buyer@example.com":
				if in.ReturnURL != "https://edge.example.com/"+subPath+"/shop" ||
					!strings.HasPrefix(in.WebhookURL, "https://203.0.113.10:21355/"+subPath+"/pay/addon/fake/") {
					t.Error("browser return and payment callback must use the edge and panel respectively")
				}
			case "buyer@example.com":
			default:
				t.Error("checkout email was not passed to the adapter")
			}
			webInvoiceKeys <- in.IdempotencyKey
			if failWebInvoice.Swap(false) {
				w.WriteHeader(http.StatusBadGateway)
				_, _ = io.WriteString(w, `{"code":"provider_unavailable","message":"try again"}`)
				return
			}
			_, _ = fmt.Fprintf(w, `{"external_id":"web-%d","pay_url":"https://pay.example/web-%d"}`, in.PaymentID, in.PaymentID)
		case r.URL.Path == "/v1/status" && strings.HasPrefix(in.ExternalID, "web-"):
			_, _ = io.WriteString(w, `{"status":"paid","amount":19900,"currency":"RUB"}`)
		case r.URL.Path == "/v1/status":
			_, _ = io.WriteString(w, `{"status":"pending","amount":19900,"currency":"RUB"}`)
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
	h.p.Addons.SetKeys(pub, pub)
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

	t.Run("web checkout", func(t *testing.T) { testWebCheckout(t, h, sale.ID, &failWebInvoice, webInvoiceKeys) })
	t.Run("relayed web checkout", func(t *testing.T) { testRelayedWebCheckout(t, h, sale.ID) })

	if resp, body := h.do(http.MethodPost, api+"/fake/remove", nil, csrf); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("remove: %d %s", resp.StatusCode, body)
	}
}

func testWebCheckout(t *testing.T, h *harness, tariffID int64, failWebInvoice *atomic.Bool, webInvoiceKeys <-chan string) {
	t.Helper()
	ctx := t.Context()
	shop := "/" + subPath + "/shop"
	if resp, body := h.do(http.MethodGet, shop, nil, nil); resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "<body>sub</body>") {
		t.Fatalf("shop page: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.do(http.MethodPost, shop+"/offers", map[string]any{}, nil); resp.StatusCode != http.StatusOK ||
		!strings.Contains(string(body), `"providers":{"stars":false}`) || !strings.Contains(string(body), `"provider":"addon:fake"`) {
		t.Fatalf("public offers: %d %s", resp.StatusCode, body)
	}
	key := strings.Repeat("a", 64)
	order := map[string]any{"checkout_key": key, "tariff_id": tariffID, "provider": "addon:fake"}
	if resp, body := h.do(http.MethodPost, shop+"/pay", order, nil); resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "invalid_email") {
		t.Fatalf("email required: %d %s", resp.StatusCode, body)
	}
	order["email"] = "buyer@example.com"
	if resp, body := h.do(http.MethodPost, shop+"/pay", order, map[string]string{"Sec-Fetch-Site": "cross-site"}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-site: %d %s", resp.StatusCode, body)
	}
	pay := func() map[string]string {
		t.Helper()
		resp, body := h.do(http.MethodPost, shop+"/pay", order, nil)
		var got map[string]string
		if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &got) != nil || got["status"] != "pending" || !strings.HasPrefix(got["url"], "https://pay.example/web-") {
			t.Fatalf("pay: %d %s", resp.StatusCode, body)
		}
		return got
	}
	expectInvoiceError := func() {
		t.Helper()
		resp, body := h.do(http.MethodPost, shop+"/pay", order, nil)
		if resp.StatusCode != http.StatusBadGateway || !strings.Contains(string(body), "invoice_failed") {
			t.Fatalf("expected an invoice failure: %d %s", resp.StatusCode, body)
		}
	}
	type checkoutStatus struct {
		Status    string `json:"status"`
		URL       string `json:"url"`
		Token     string `json:"token"`
		Retryable bool   `json:"retryable"`
	}
	status := func(key string) checkoutStatus {
		t.Helper()
		resp, body := h.do(http.MethodPost, shop+"/status", map[string]string{"checkout_key": key}, nil)
		var got checkoutStatus
		if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &got) != nil {
			t.Fatalf("status: %d %s", resp.StatusCode, body)
		}
		return got
	}
	failWebInvoice.Store(true)
	expectInvoiceError()
	if got := status(key); got.Status != "failed" || !got.Retryable || got.URL != "" {
		t.Fatalf("before reopening the invoice: %+v", got)
	}
	delete(order, "email") // The browser can resume after a reload using the saved order fields.

	// The provider succeeds on the next attempt, but PostgreSQL refuses the URL.
	setConstraint := func(query string) {
		t.Helper()
		if _, err := h.st.DB.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	setConstraint("ALTER TABLE payments ADD CONSTRAINT test_no_web_payment_url CHECK (pay_url NOT LIKE 'https://pay.example/web-%')")
	expectInvoiceError()
	setConstraint("ALTER TABLE payments DROP CONSTRAINT test_no_web_payment_url")

	first := pay()
	if again := pay(); again["url"] != first["url"] {
		t.Fatalf("retry created a different invoice: %v, %v", first, again)
	}
	order["email"] = "other@example.com"
	if resp, body := h.do(http.MethodPost, shop+"/pay", order, nil); resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "checkout_conflict") {
		t.Fatalf("changed email on replay: %d %s", resp.StatusCode, body)
	}
	order["email"] = "buyer@example.com"
	if len(webInvoiceKeys) != 3 {
		t.Fatalf("provider got %d invoice requests, want 3", len(webInvoiceKeys))
	}
	wantIdempotency := <-webInvoiceKeys
	if wantIdempotency == "" || <-webInvoiceKeys != wantIdempotency || <-webInvoiceKeys != wantIdempotency {
		t.Fatal("failed invoice retries did not reuse the original payment idempotency key")
	}
	if got := status(key); got.Status != "pending" || got.Token != "" || got.Retryable {
		t.Fatalf("before payment: %v", got)
	}
	if resp, _ := h.do(http.MethodPost, shop+"/status", map[string]string{"checkout_key": strings.Repeat("b", 64)}, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("wrong key: %d", resp.StatusCode)
	}
	h.p.Billing.Reconcile(ctx)
	got := status(key)
	if got.Status != "applied" || got.Token == "" {
		t.Fatalf("after payment: %v", got)
	}
	u, err := h.st.Q.GetUserBySubToken(ctx, got.Token)
	if err != nil || u.Source != domain.UserFromWeb || u.Contact != "buyer@example.com" {
		t.Fatalf("web subscriber: source %s, contact %q, error %v", u.Source, u.Contact, err)
	}
	if _, err := h.st.Q.GetTgLink(ctx, u.ID); err != sql.ErrNoRows {
		t.Fatalf("subscriber has a Telegram link before linking: %v", err)
	}

	order["checkout_key"] = strings.Repeat("c", 64)
	order["token"] = strings.Repeat("z", 24)
	if resp, body := h.do(http.MethodPost, shop+"/pay", order, nil); resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "not_yours") {
		t.Fatalf("wrong renewal token: %d %s", resp.StatusCode, body)
	}
	order["token"] = u.SubToken
	pay()
	h.p.Billing.Reconcile(ctx)
	renewed, err := h.st.Q.GetUser(ctx, u.ID)
	if err != nil || renewed.SubToken != u.SubToken || renewed.ExpiresAt.Int64 <= u.ExpiresAt.Int64 {
		t.Fatalf("renewal: expiry %v, error %v", renewed.ExpiresAt, err)
	}
	if got := status(order["checkout_key"].(string)); got.Status != "applied" || got.Token != "" {
		t.Fatalf("renewal status: %v", got)
	}
	if resp, body := h.do(http.MethodPost, fmt.Sprintf("/%s/api/v1/users/%d/reissue", adminPath, u.ID), nil, map[string]string{"X-CSRF-Token": h.csrf}); resp.StatusCode != http.StatusOK {
		t.Fatalf("reissue: %d %s", resp.StatusCode, body)
	}
	if got := status(key); got.Status != "applied" || got.Token != "" {
		t.Fatalf("rotated token is accessible from the old checkout: %v", got)
	}

	expiredKey := strings.Repeat("d", 64)
	order["checkout_key"] = expiredKey
	delete(order, "token")
	failWebInvoice.Store(true)
	expectInvoiceError()
	payload := fmt.Sprintf("web_%x", sha256.Sum256([]byte(expiredKey)))
	_, err = h.st.DB.ExecContext(ctx, "UPDATE payments SET created_at = $1, status = 'pending' WHERE payload = $2", h.now.Add(-48*time.Hour).Unix(), payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.DB.ExecContext(ctx, `INSERT INTO web_invoice_leases (payment_id, lease_until)
		SELECT id, $1 FROM payments WHERE payload = $2`, h.now.Add(time.Minute).Unix(), payload); err != nil {
		t.Fatal(err)
	}
	h.p.Billing.Reconcile(ctx)
	if got := status(expiredKey); got.Status != "pending" || !got.Retryable {
		t.Fatalf("reconcile expired an invoice still being opened: %+v", got)
	}
	if resp, body := h.do(http.MethodPost, shop+"/pay", order, nil); resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "invoice_in_progress") {
		t.Fatalf("retry while invoice is being opened: %d %s", resp.StatusCode, body)
	}
	if _, err := h.st.DB.ExecContext(ctx, `UPDATE web_invoice_leases SET lease_until = $1
		WHERE payment_id = (SELECT id FROM payments WHERE payload = $2)`, h.now.Add(-time.Minute).Unix(), payload); err != nil {
		t.Fatal(err)
	}
	calls := len(webInvoiceKeys)
	resp, body := h.do(http.MethodPost, shop+"/pay", order, nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"status":"expired"`) {
		t.Fatalf("expired checkout: %d %s", resp.StatusCode, body)
	}
	if got := status(expiredKey); got.Status != "expired" || got.URL != "" || got.Retryable || len(webInvoiceKeys) != calls {
		t.Fatalf("expired invoice was reopened or its status was not saved: %v", got)
	}
}

func testRelayedWebCheckout(t *testing.T, h *harness, tariffID int64) {
	t.Helper()
	ctx := t.Context()
	set := settings.New(h.st.Q)
	if err := settings.Set(ctx, set, settings.KeySubDomain, "edge.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, set, settings.KeySubExternal, true); err != nil {
		t.Fatal(err)
	}

	api := "/" + adminPath + "/api/v1/api-keys"
	resp, body := h.do(http.MethodPost, api, map[string]any{"name": "edge", "scope": "subscription", "password": password}, map[string]string{"X-CSRF-Token": h.csrf})
	var key struct {
		Key string `json:"key"`
	}
	if resp.StatusCode != http.StatusCreated || json.Unmarshal(body, &key) != nil || key.Key == "" {
		t.Fatalf("create relay key: %d %s", resp.StatusCode, body)
	}
	shop := "/" + subPath + "/relay/shop"
	if resp, _ := h.do(http.MethodGet, shop, nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unauthenticated relay: %d", resp.StatusCode)
	}
	headers := map[string]string{"Authorization": "Bearer " + key.Key, "Origin": "https://edge.example.com", "X-Mikan-Client-IP": "192.0.2.1"}
	if resp, body := h.do(http.MethodGet, shop, nil, headers); resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "<body>sub</body>") {
		t.Fatalf("relayed shop: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.do(http.MethodPost, shop+"/offers", map[string]any{}, headers); resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "addon:fake") {
		t.Fatalf("relayed offers: %d %s", resp.StatusCode, body)
	}
	checkoutKey := strings.Repeat("e", 64)
	order := map[string]any{"checkout_key": checkoutKey, "tariff_id": tariffID, "email": "edge-buyer@example.com", "provider": "addon:fake"}
	if resp, body := h.do(http.MethodPost, shop+"/pay", order, headers); resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"url":"https://pay.example/web-`) {
		t.Fatalf("relayed payment: %d %s", resp.StatusCode, body)
	}

	h.p.Billing.Reconcile(ctx)
	resp, body = h.do(http.MethodPost, shop+"/status", map[string]string{"checkout_key": checkoutKey}, headers)
	var purchase struct {
		Status string `json:"status"`
		Token  string `json:"token"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &purchase) != nil || purchase.Status != "applied" || purchase.Token == "" {
		t.Fatalf("relayed checkout status: %d %s", resp.StatusCode, body)
	}
	user, err := h.st.Q.GetUserBySubToken(ctx, purchase.Token)
	if err != nil || user.Contact != order["email"] {
		t.Fatalf("relayed purchase contact: %q, %v", user.Contact, err)
	}

	// A polled key gets more requests than the catalog. Both have their own IP budget.
	for _, tc := range []struct {
		path string
		max  int
	}{
		{"offers", 60},
		{"status", 120},
	} {
		headers["X-Mikan-Client-IP"] = "192.0.2.2"
		for n := range tc.max + 1 {
			resp, _ := h.do(http.MethodPost, shop+"/"+tc.path, map[string]string{"checkout_key": checkoutKey}, headers)
			if n < tc.max && resp.StatusCode != http.StatusOK {
				t.Fatalf("%s request %d: %d", tc.path, n+1, resp.StatusCode)
			}
			if n == tc.max && (resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "") {
				t.Fatalf("%s should be rate-limited: %d", tc.path, resp.StatusCode)
			}
		}
	}
}
