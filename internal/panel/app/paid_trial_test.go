package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mikan/internal/panel/addons"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/tgbot"
)

// A priced trial over HTTP: the admin sets its price, the Mini App offers it to an
// account without a subscription, the card payment gives the trial subscription, and the
// offer is gone after that.
func TestPaidTrialOverHTTP(t *testing.T) {
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
	trial := ts[0]
	api := "/" + adminPath + "/api/v1"
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	if resp, body := h.do(http.MethodPatch, api+"/addons/yookassa", map[string]any{"enabled": true, "settings": map[string]any{"shop_id": ykShop, "secret_key": ykSecret}}, csrf); resp.StatusCode != http.StatusOK {
		t.Fatalf("keys: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.do(http.MethodPatch, api+"/payments/settings", map[string]any{"trial_rub": -1}, csrf); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a negative price: %d %s", resp.StatusCode, body)
	}
	// Selling on for the trial alone: no tariff is on sale, new subscriptions are off.
	resp, body := h.do(http.MethodPatch, api+"/payments/settings", map[string]any{"enabled": true, "allow_new": false, "trial_tariff_id": trial.ID, "trial_rub": 100}, csrf)
	var ps struct {
		TrialRub   int64 `json:"trial_rub"`
		TrialStars int64 `json:"trial_stars"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &ps) != nil || ps.TrialRub != 100 || ps.TrialStars != 0 {
		t.Fatalf("trial price: %d %s", resp.StatusCode, body)
	}

	same := map[string]string{"Sec-Fetch-Site": "same-origin"}
	shop := "/" + subPath + "/tg/shop"
	pay := "/" + subPath + "/tg/pay"
	type shopView struct {
		Offers []json.RawMessage `json:"offers"`
		Trial  *struct {
			ID    int64  `json:"id"`
			Name  string `json:"name"`
			Rub   int64  `json:"rub"`
			Stars int64  `json:"stars"`
		} `json:"trial"`
	}
	var sv shopView
	resp, body = h.do(http.MethodPost, shop, map[string]any{"init_data": initData(tgToken, 556, h.now)}, same)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &sv) != nil || len(sv.Offers) != 0 || sv.Trial == nil || sv.Trial.ID != trial.ID || sv.Trial.Rub != 100 || sv.Trial.Stars != 0 {
		t.Fatalf("shop with a priced trial: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.do(http.MethodPost, pay, map[string]any{"init_data": initData(tgToken, 556, h.now), "trial": true, "provider": "addon:yookassa", "promo_code": "X"}, same); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a promo code on the trial: %d %s", resp.StatusCode, body)
	}
	resp, body = h.do(http.MethodPost, pay, map[string]any{"init_data": initData(tgToken, 556, h.now), "trial": true, "provider": "addon:yookassa"}, same)
	var inv struct {
		URL string `json:"url"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &inv) != nil || inv.URL != "https://yoomoney.ru/checkout/yk-1" {
		t.Fatalf("trial invoice: %d %s", resp.StatusCode, body)
	}
	yk.paid("yk-1")
	h.p.Billing.Reconcile(ctx)
	resp, body = h.do(http.MethodGet, api+"/payments?status=applied", nil, nil)
	var hist struct {
		Items []struct {
			ID   int64  `json:"id"`
			Kind string `json:"kind"`
		} `json:"items"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &hist) != nil || len(hist.Items) != 1 || hist.Items[0].Kind != "trial" {
		t.Fatalf("history: %d %s", resp.StatusCode, body)
	}
	p, err := h.st.Q.GetPayment(ctx, hist.Items[0].ID)
	if err != nil || p.Amount != 100 || !p.UserID.Valid {
		t.Fatalf("trial payment %+v %v", p, err)
	}
	u, err := h.st.Q.GetUser(ctx, p.UserID.Int64)
	if err != nil || u.TariffID.Int64 != trial.ID || u.Source != domain.UserFromTrial {
		t.Fatalf("trial user %+v %v", u, err)
	}
	sv = shopView{}
	resp, body = h.do(http.MethodPost, shop, map[string]any{"init_data": initData(tgToken, 556, h.now)}, same)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &sv) != nil || sv.Trial != nil {
		t.Fatalf("the trial is offered after it was bought: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.do(http.MethodPost, pay, map[string]any{"init_data": initData(tgToken, 556, h.now), "trial": true, "provider": "addon:yookassa"}, same); resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "trial_used") {
		t.Fatalf("a second trial: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.do(http.MethodGet, api+"/payments/settings", nil, nil); resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"trials":1`) {
		t.Fatalf("trials counted: %d %s", resp.StatusCode, body)
	}
}
