package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
)

// The official sing-box apps get a sing-box config, the others the links as before.
func TestSingBoxOverHTTP(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]any{settings.KeyPublicHost: "203.0.113.10", settings.KeyDomain: "vpn.example.com", settings.KeyPanelPort: 21355} {
		if err := settings.Set(ctx, settings.New(h.st.Q), k, v); err != nil {
			t.Fatal(err)
		}
	}
	clock := func() time.Time { return h.now }
	tariffs, _ := h.st.Q.ListTariffs(ctx)
	u, err := domain.NewUsers(h.st, domain.NewPool(h.st, clock), noChanges{}, clock).Create(ctx, domain.CreateInput{Name: "a", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	fetch := func(ua string) (*http.Response, []byte) {
		h.advance(time.Minute)
		return h.do(http.MethodGet, "/"+subPath+"/"+u.SubToken, nil, map[string]string{"User-Agent": ua})
	}
	resp, body := fetch("SFA (sing-box 1.14.2; language en_US)")
	var cfg struct {
		Outbounds []map[string]any `json:"outbounds"`
		Route     struct {
			Final string `json:"final"`
		} `json:"route"`
	}
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") || json.Unmarshal(body, &cfg) != nil ||
		len(cfg.Outbounds) < 3 || cfg.Route.Final != "VPN" {
		t.Fatalf("SFA: %d %s %.300s", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	if resp, body := fetch("HiddifyNext/4.1.1 (android) like ClashMeta v2ray sing-box"); resp.StatusCode != http.StatusOK || strings.HasPrefix(string(body), "{") {
		t.Fatalf("Hiddify keeps the links: %.200s", body)
	}
}
