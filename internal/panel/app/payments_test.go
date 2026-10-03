package app

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"mikan/internal/panel/addons"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/tgbot"
)

const (
	ykShop   = "506751"
	ykSecret = "test_Fh8hUAVVBGUGbjmlzba6TB0iyUbos_lueTHE-axOwM0"
)

// ykAdapter is the marketplace's YooKassa adapter as far as the panel uses it: its keys,
// invoices with YooKassa's payment links, statuses the test sets, and notifications only
// from YooKassa's addresses.
type ykAdapter struct {
	mu    sync.Mutex
	pays  map[string]addons.Status
	count int
	// failCreate answers a new invoice with the adapter's own failure.
	failCreate bool
}

func (f *ykAdapter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fail := func(status int, code string) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "message": code})
	}
	if r.Header.Get("Authorization") != "Bearer yk-token" {
		fail(http.StatusUnauthorized, "unauthorized")
		return
	}
	if r.URL.Path == "/v1/info" {
		_, _ = w.Write([]byte(`{"id":"yookassa","protocol":1,"version":"1.0.1","name":{"ru":"ЮKassa","en":"YooKassa"},"currencies":["RUB"],"capabilities":["webhook"],
			"settings":[{"key":"shop_id","type":"string","required":true,"pattern":"^[0-9]{1,20}$"},{"key":"secret_key","type":"string","secret":true,"required":true}]}`))
		return
	}
	var in struct {
		Settings   addons.Settings `json:"settings"`
		Amount     int64           `json:"amount"`
		Currency   string          `json:"currency"`
		ExternalID string          `json:"external_id"`
		RemoteIP   string          `json:"remote_ip"`
		Body       string          `json:"body"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	if in.Settings["shop_id"] != ykShop || in.Settings["secret_key"] != ykSecret {
		fail(http.StatusUnprocessableEntity, "bad_credentials")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Path {
	case "/v1/check":
		_, _ = w.Write([]byte(`{}`))
	case "/v1/invoices":
		if f.failCreate {
			fail(http.StatusBadGateway, "provider_error")
			return
		}
		f.count++
		id := "yk-" + strconv.Itoa(f.count)
		f.pays[id] = addons.Status{Status: "pending", Amount: in.Amount, Currency: in.Currency}
		_ = json.NewEncoder(w).Encode(addons.Invoice{ExternalID: id, PayURL: "https://yoomoney.ru/checkout/" + id})
	case "/v1/status":
		_ = json.NewEncoder(w).Encode(f.pays[in.ExternalID])
	case "/v1/webhook":
		if !strings.HasPrefix(in.RemoteIP, "185.71.76.") {
			fail(http.StatusBadRequest, "bad_request")
			return
		}
		raw, _ := base64.StdEncoding.DecodeString(in.Body)
		var n struct {
			Object struct {
				ID string `json:"id"`
			} `json:"object"`
		}
		_ = json.Unmarshal(raw, &n)
		_ = json.NewEncoder(w).Encode(map[string]string{"external_id": n.Object.ID})
	default:
		fail(http.StatusNotFound, "not_found")
	}
}

func (f *ykAdapter) paid(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.pays[id]
	s.Status = "paid"
	f.pays[id] = s
}

// Payments over HTTP: an adapter's keys are checked before they are saved and never come
// back, only an admin session sets them, the Mini App sells to the signed-in account only,
// and what clients and YooKassa knew from the built-in provider still works.
func TestPaymentsOverHTTP(t *testing.T) {
	yk := &ykAdapter{pays: map[string]addons.Status{}}
	srv := httptest.NewServer(yk)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	state, _ := json.Marshal(addons.State{Adapters: map[string]addons.Installed{"yookassa": {Version: "1.0.1", Digest: "sha256:1", Status: "running",
		Listen: strings.TrimPrefix(srv.URL, "http://"), Token: "yk-token"}}})
	if err := os.MkdirAll(filepath.Join(dir, "addons"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "addons", "state.json"), state, 0o600); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, func(o *Options) { o.DataDir = dir })
	ctx := context.Background()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	set := settings.New(h.st.Q)
	for k, v := range map[string]any{settings.KeyPublicHost: "203.0.113.10", settings.KeyPanelPort: 21355, tgbot.KeyToken: tgToken} {
		if err := settings.Set(ctx, set, k, v); err != nil {
			t.Fatal(err)
		}
	}
	ts, _ := h.st.Q.ListTariffs(ctx)
	sale, err := h.st.Q.UpdateTariff(ctx, db.UpdateTariffParams{Name: "Месяц", TrafficLimit: ts[1].TrafficLimit, DurationDays: 30, DeviceLimit: ts[1].DeviceLimit,
		ResetStrategy: ts[1].ResetStrategy, PriceRub: sql.NullInt64{Int64: 19900, Valid: true}, OnSale: 1, ID: ts[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	api := "/" + adminPath + "/api/v1"
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}

	// A tariff on sale needs a price.
	if resp, body := h.do(http.MethodPut, api+"/tariffs/"+strconv.FormatInt(sale.ID, 10), map[string]any{"name": "x", "duration_days": 30, "reset_strategy": "none", "on_sale": true}, csrf); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "on_sale_no_price") {
		t.Fatalf("on sale without a price: %d %s", resp.StatusCode, body)
	}
	// Selling starts off; with it on, nothing takes rubles until the adapter is set up.
	if resp, body := h.do(http.MethodGet, api+"/payments/settings", nil, nil); resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"on_sale":0`) || !strings.Contains(string(body), `"enabled":false`) {
		t.Fatalf("nothing on sale: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.do(http.MethodPatch, api+"/payments/settings", map[string]any{"enabled": true}, csrf); resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"on_sale":0`) {
		t.Fatalf("selling on: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.do(http.MethodPatch, api+"/addons/yookassa", map[string]any{"enabled": true, "settings": map[string]any{"shop_id": ykShop, "secret_key": "test_wrong"}}, csrf); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "bad_credentials") {
		t.Fatalf("wrong secret: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.do(http.MethodPatch, api+"/addons/yookassa", map[string]any{"enabled": true, "settings": map[string]any{"shop_id": ykShop, "secret_key": ykSecret}}, csrf); resp.StatusCode != http.StatusOK || strings.Contains(string(body), ykSecret) {
		t.Fatalf("keys: %d %s", resp.StatusCode, body)
	}
	resp, body := h.do(http.MethodGet, api+"/payments/settings", nil, nil)
	var ps struct {
		OnSale    int `json:"on_sale"`
		Available struct {
			Addons []string `json:"addons"`
		} `json:"available"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &ps) != nil || ps.OnSale != 1 || len(ps.Available.Addons) != 1 || ps.Available.Addons[0] != "yookassa" {
		t.Fatalf("settings: %d %s", resp.StatusCode, body)
	}

	// A key may read payments but never touch where the money goes.
	resp, body = h.do(http.MethodPost, api+"/api-keys", map[string]any{"name": "billing", "scope": "full", "password": password}, csrf)
	var key struct {
		Key string `json:"key"`
	}
	if resp.StatusCode != http.StatusCreated || json.Unmarshal(body, &key) != nil {
		t.Fatalf("key: %d %s", resp.StatusCode, body)
	}
	bearer := map[string]string{"Authorization": "Bearer " + key.Key}
	if resp, _ := h.do(http.MethodGet, api+"/payments", nil, bearer); resp.StatusCode != http.StatusOK {
		t.Fatalf("key reads payments: %d", resp.StatusCode)
	}
	for _, c := range []struct{ method, path string }{{http.MethodGet, "/payments/settings"}, {http.MethodPatch, "/payments/settings"}, {http.MethodGet, "/addons"},
		{http.MethodPatch, "/addons/yookassa"}, {http.MethodPost, "/addons/yookassa/remove"}} {
		if resp, _ := h.do(c.method, api+c.path, map[string]any{"enabled": false}, bearer); resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s %s with a key: %d", c.method, c.path, resp.StatusCode)
		}
	}

	// The Mini App: the plans, then an invoice for the signed-in account.
	same := map[string]string{"Sec-Fetch-Site": "same-origin"}
	shop := "/" + subPath + "/tg/shop"
	pay := "/" + subPath + "/tg/pay"
	resp, body = h.do(http.MethodPost, shop, map[string]any{"init_data": initData(tgToken, 555, h.now)}, same)
	var offers struct {
		Offers []struct {
			ID  int64 `json:"id"`
			Rub int64 `json:"rub"`
		} `json:"offers"`
		Providers map[string]bool `json:"providers"`
		Addons    []struct {
			Provider string `json:"provider"`
		} `json:"addons"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &offers) != nil || len(offers.Offers) != 1 || offers.Offers[0].Rub != 19900 || offers.Providers["stars"] ||
		len(offers.Addons) != 1 || offers.Addons[0].Provider != "addon:yookassa" {
		t.Fatalf("shop: %d %s", resp.StatusCode, body)
	}
	if resp, _ := h.do(http.MethodPost, shop, map[string]any{"init_data": initData("987654321:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw1", 555, h.now)}, same); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("another bot's signature: %d", resp.StatusCode)
	}
	if resp, _ := h.do(http.MethodPost, pay, map[string]any{"init_data": initData(tgToken, 555, h.now), "tariff_id": sale.ID, "provider": "addon:yookassa"}, map[string]string{"Sec-Fetch-Site": "cross-site"}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("from another site: %d", resp.StatusCode)
	}
	// Renewing someone else's subscription by its token is refused.
	clock := h.p.now
	other, err := domain.NewUsers(h.st, domain.NewPool(h.st, clock), noChanges{}, clock).Create(ctx, domain.CreateInput{Name: "other", TariffID: sale.ID})
	if err != nil {
		t.Fatal(err)
	}
	if resp, body := h.do(http.MethodPost, pay, map[string]any{"init_data": initData(tgToken, 555, h.now), "tariff_id": sale.ID, "provider": "addon:yookassa", "token": other.SubToken}, same); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("someone else's subscription: %d %s", resp.StatusCode, body)
	}
	// A Mini App opened before the update still says "yookassa": it pays through the adapter.
	resp, body = h.do(http.MethodPost, pay, map[string]any{"init_data": initData(tgToken, 555, h.now), "tariff_id": sale.ID, "provider": "yookassa"}, same)
	var inv struct {
		URL      string `json:"url"`
		Provider string `json:"provider"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &inv) != nil || inv.URL != "https://yoomoney.ru/checkout/yk-1" || inv.Provider != "addon:yookassa" {
		t.Fatalf("pay: %d %s", resp.StatusCode, body)
	}

	// A notification at the URL YooKassa's dashboard has had since 0.4.0, but from outside
	// its networks: the adapter refuses it at the door.
	tok, err := h.p.Billing.WebhookToken(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resp, _ = h.do(http.MethodPost, "/"+subPath+"/pay/yookassa/"+tok, map[string]any{"event": "payment.succeeded", "object": map[string]any{"id": "yk-1"}}, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("webhook from 127.0.0.1: %d", resp.StatusCode)
	}
	// Paid at YooKassa: the reconcile pass applies it and the history shows it.
	yk.paid("yk-1")
	h.p.Billing.Reconcile(ctx)
	resp, body = h.do(http.MethodGet, api+"/payments?status=applied", nil, nil)
	var hist struct {
		Items []struct {
			ID         int64  `json:"id"`
			UserName   string `json:"user_name"`
			TgUsername string `json:"tg_username"`
			Kind       string `json:"kind"`
			Provider   string `json:"provider"`
		} `json:"items"`
		Totals []struct {
			Currency string `json:"currency"`
			Total    int64  `json:"total"`
		} `json:"totals"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &hist) != nil || len(hist.Items) != 1 || hist.Items[0].Kind != "new" || hist.Items[0].UserName == "" ||
		hist.Items[0].Provider != "addon:yookassa" || len(hist.Totals) != 1 || hist.Totals[0].Total != 19900 {
		t.Fatalf("history: %d %s", resp.StatusCode, body)
	}
	// Only Stars are refunded from the panel.
	if resp, _ := h.do(http.MethodPost, api+"/payments/"+strconv.FormatInt(hist.Items[0].ID, 10)+"/refund", nil, csrf); resp.StatusCode != http.StatusConflict {
		t.Fatalf("refund of a card payment: %d", resp.StatusCode)
	}

	// A traffic package in the Mini App for the subscription just bought, paid by card.
	pay1, _ := h.st.Q.GetPayment(ctx, hist.Items[0].ID)
	mine, _ := h.st.Q.GetUser(ctx, pay1.UserID.Int64)
	var pk struct {
		ID int64 `json:"id"`
	}
	resp, body = h.do(http.MethodPost, api+"/packages", map[string]any{"name": "+50 ГБ", "bytes": 50 << 30, "lifetime": "used", "price_rub": 7900, "on_sale": true}, csrf)
	if resp.StatusCode != http.StatusCreated || json.Unmarshal(body, &pk) != nil {
		t.Fatalf("package: %d %s", resp.StatusCode, body)
	}
	resp, body = h.do(http.MethodPost, shop, map[string]any{"init_data": initData(tgToken, 555, h.now)}, same)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"packages":[]`) {
		t.Fatalf("packages without a subscription chosen: %d %s", resp.StatusCode, body)
	}
	resp, body = h.do(http.MethodPost, shop, map[string]any{"init_data": initData(tgToken, 555, h.now), "token": mine.SubToken}, same)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"packages":[{"id":`+strconv.FormatInt(pk.ID, 10)) || !strings.Contains(string(body), `"rub":7900`) {
		t.Fatalf("packages: %d %s", resp.StatusCode, body)
	}
	if resp, _ := h.do(http.MethodPost, pay, map[string]any{"init_data": initData(tgToken, 555, h.now), "package_id": pk.ID, "provider": "addon:yookassa", "token": other.SubToken}, same); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a package for someone else's subscription: %d", resp.StatusCode)
	}
	resp, body = h.do(http.MethodPost, pay, map[string]any{"init_data": initData(tgToken, 555, h.now), "package_id": pk.ID, "provider": "addon:yookassa", "token": mine.SubToken}, same)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &inv) != nil || inv.URL != "https://yoomoney.ru/checkout/yk-2" {
		t.Fatalf("package pay: %d %s", resp.StatusCode, body)
	}
	yk.paid("yk-2")
	h.p.Billing.Reconcile(ctx)
	h.p.Billing.Reconcile(ctx)
	if gs, _ := h.st.Q.ListUserGrants(ctx, mine.ID); len(gs) != 1 || gs[0].Bytes != 50<<30 {
		t.Fatalf("grants after the card payment: %+v", gs)
	}
	if _, body := h.do(http.MethodGet, api+"/payments?status=applied", nil, nil); !strings.Contains(string(body), `"kind":"package"`) {
		t.Fatalf("history: %s", body)
	}

	// The plan for several terms: the Mini App lists them, and pays for the one chosen.
	termsBody := map[string]any{"name": "Месяц", "reset_strategy": sale.ResetStrategy, "duration_days": 30, "on_sale": true,
		"terms": []map[string]any{{"days": 30, "price_rub": 19900}, {"days": 90, "price_rub": 49900}}}
	if resp, body := h.do(http.MethodPut, api+"/tariffs/"+strconv.FormatInt(sale.ID, 10), termsBody, csrf); resp.StatusCode != http.StatusOK {
		t.Fatalf("terms: %d %s", resp.StatusCode, body)
	}
	resp, body = h.do(http.MethodPost, shop, map[string]any{"init_data": initData(tgToken, 555, h.now)}, same)
	var withTerms struct {
		Offers []struct {
			ID    int64 `json:"id"`
			Rub   int64 `json:"rub"`
			Terms []struct {
				Days  int64  `json:"days"`
				Label string `json:"label"`
				Rub   int64  `json:"rub"`
			} `json:"terms"`
		} `json:"offers"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &withTerms) != nil || len(withTerms.Offers) != 1 || len(withTerms.Offers[0].Terms) != 2 ||
		withTerms.Offers[0].Terms[1].Days != 90 || withTerms.Offers[0].Terms[1].Rub != 49900 || withTerms.Offers[0].Rub != 19900 || withTerms.Offers[0].Terms[1].Label == "" {
		t.Fatalf("shop with terms: %d %s", resp.StatusCode, body)
	}
	resp, body = h.do(http.MethodPost, pay, map[string]any{"init_data": initData(tgToken, 555, h.now), "tariff_id": sale.ID, "term_days": 90, "provider": "addon:yookassa"}, same)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &inv) != nil || inv.URL != "https://yoomoney.ru/checkout/yk-3" {
		t.Fatalf("pay for 90 days: %d %s", resp.StatusCode, body)
	}
	yk.mu.Lock()
	amount := yk.pays["yk-3"].Amount
	yk.mu.Unlock()
	if amount != 49900 {
		t.Fatalf("90 days invoiced for %d", amount)
	}
	if resp, body := h.do(http.MethodPost, pay, map[string]any{"init_data": initData(tgToken, 555, h.now), "tariff_id": sale.ID, "term_days": 14, "provider": "addon:yookassa"}, same); resp.StatusCode == http.StatusOK {
		t.Fatalf("a term the plan has not: %d %s", resp.StatusCode, body)
	}
	if _, body := h.do(http.MethodGet, api+"/payments?status=pending", nil, nil); !strings.Contains(string(body), `"term_days":90`) {
		t.Fatalf("the payment's term: %s", body)
	}
}
