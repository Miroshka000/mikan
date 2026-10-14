package app

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/tgate"
)

// The free plan over HTTP: the admin picks its tariff (one with a term) and may not ask
// for a channel the bot does not have; the Mini App offers it, gives a subscription from
// the free plan, refuses a second while the first runs, and the settings count it.
func TestFreePlanOverHTTP(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	set := settings.New(h.st.Q)
	for k, v := range map[string]any{settings.KeyPublicHost: "203.0.113.10", settings.KeyPanelPort: 21355, tgate.KeyToken: tgToken} {
		if err := settings.Set(ctx, set, k, v); err != nil {
			t.Fatal(err)
		}
	}
	ts, _ := h.st.Q.ListTariffs(ctx)
	free := ts[0]
	api := "/" + adminPath + "/api/v1"
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	if _, err := h.st.DB.ExecContext(ctx, "UPDATE tariffs SET duration_days = 0 WHERE id = $1", ts[1].ID); err != nil {
		t.Fatal(err)
	}
	if resp, body := h.do(http.MethodPatch, api+"/payments/settings", map[string]any{"free_tariff_id": ts[1].ID}, csrf); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "free_no_term") {
		t.Fatalf("a free plan without a term: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.do(http.MethodPatch, api+"/payments/settings", map[string]any{"free_tariff_id": free.ID, "free_condition": "channel"}, csrf); resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "free_no_channel") {
		t.Fatalf("a channel the bot does not have: %d %s", resp.StatusCode, body)
	}
	resp, body := h.do(http.MethodPatch, api+"/payments/settings", map[string]any{"free_tariff_id": free.ID, "free_condition": "none"}, csrf)
	var ps struct {
		FreeTariffID  *int64 `json:"free_tariff_id"`
		FreeCondition string `json:"free_condition"`
		FreePlans     int64  `json:"free_plans"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &ps) != nil || ps.FreeTariffID == nil || *ps.FreeTariffID != free.ID || ps.FreeCondition != "none" {
		t.Fatalf("the free plan: %d %s", resp.StatusCode, body)
	}

	same := map[string]string{"Sec-Fetch-Site": "same-origin"}
	auth := map[string]any{"init_data": initData(tgToken, 557, h.now)}
	var sv struct {
		Free *struct {
			Name    string `json:"name"`
			Channel string `json:"channel"`
		} `json:"free"`
	}
	resp, body = h.do(http.MethodPost, "/"+subPath+"/tg/shop", auth, same)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &sv) != nil || sv.Free == nil || sv.Free.Name != free.Name || sv.Free.Channel != "" {
		t.Fatalf("shop with the free plan: %d %s", resp.StatusCode, body)
	}
	resp, body = h.do(http.MethodPost, "/"+subPath+"/tg/free", auth, same)
	var got struct {
		Token string `json:"token"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &got) != nil || got.Token == "" {
		t.Fatalf("the free plan taken: %d %s", resp.StatusCode, body)
	}
	u, err := h.st.Q.GetUserBySubToken(ctx, got.Token)
	if err != nil || u.TariffID.Int64 != free.ID || u.Source != domain.UserFromFree {
		t.Fatalf("free user %+v %v", u, err)
	}
	if resp, body := h.do(http.MethodPost, "/"+subPath+"/tg/free", auth, same); resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "free_active") {
		t.Fatalf("a second free plan: %d %s", resp.StatusCode, body)
	}
	sv.Free = nil
	if resp, body := h.do(http.MethodPost, "/"+subPath+"/tg/shop", auth, same); resp.StatusCode != http.StatusOK || json.Unmarshal(body, &sv) != nil || sv.Free != nil {
		t.Fatalf("the free plan is offered while the first runs: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.do(http.MethodGet, api+"/payments/settings", nil, nil); resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"free_plans":1`) {
		t.Fatalf("free plans counted: %d %s", resp.StatusCode, body)
	}
	if resp, body := h.do(http.MethodGet, api+"/users?source=free", nil, nil); resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"source":"free"`) {
		t.Fatalf("users from the free plan: %d %s", resp.StatusCode, body)
	}
}
