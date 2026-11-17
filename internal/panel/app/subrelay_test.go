package app

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/dnscheck"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
)

func TestSubscriptionRelayKey(t *testing.T) {
	h := newHarness(t)
	if resp, _ := h.login(password, ""); resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	api := "/" + adminPath + "/api/v1"
	resp, body := h.do("POST", api+"/api-keys", map[string]any{"name": "edge", "scope": "subscription", "expire_days": 1, "password": password}, map[string]string{"X-CSRF-Token": h.csrf})
	var key struct {
		Key string `json:"key"`
		ID  int64  `json:"id"`
	}
	if resp.StatusCode != http.StatusCreated || json.Unmarshal(body, &key) != nil {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}

	resp, body = h.do("POST", api+"/api-keys", map[string]any{"name": "general", "scope": "full", "password": password}, map[string]string{"X-CSRF-Token": h.csrf})
	var full struct {
		Key string `json:"key"`
	}
	if resp.StatusCode != 201 || json.Unmarshal(body, &full) != nil {
		t.Fatalf("full key: %d %s", resp.StatusCode, body)
	}
	if resp, _ := h.do("GET", "/api/subscription/abcdefghijklmnopqrstuvwx", nil, map[string]string{"Authorization": "Bearer " + full.Key}); resp.StatusCode != 403 {
		t.Fatalf("general key reached relay: %d", resp.StatusCode)
	}
	if resp, _ := h.do("GET", "/api/subscription/abcdefghijklmnopqrstuvwx", nil, nil); resp.StatusCode != 401 {
		t.Fatalf("admin cookie reached relay: %d", resp.StatusCode)
	}

	ctx := t.Context()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return h.now }
	tariffs, _ := h.st.Q.ListTariffs(ctx)
	u, err := domain.NewUsers(h.st, domain.NewPool(h.st, clock), noChanges{}, clock).Create(ctx, domain.CreateInput{Name: "edge-user", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	jar := h.client.Jar
	h.client.Jar = nil
	bearer := map[string]string{"Authorization": "Bearer " + key.Key}
	for _, p := range []string{"/users", "/settings", "/nodes", "/api-keys"} {
		if resp, _ := h.do("GET", api+p, nil, bearer); resp.StatusCode != 403 {
			t.Errorf("scope escaped to %s: %d", p, resp.StatusCode)
		}
	}

	bearer["User-Agent"] = "mihomo/1.19.32"
	if resp, body := h.do("GET", "/api/subscription/"+u.SubToken, nil, bearer); resp.StatusCode != 200 || len(body) == 0 {
		t.Fatalf("profile: %d %s", resp.StatusCode, body)
	}
	// Unknown paths and unknown subscriber tokens are refused.
	for _, p := range []string{"/admin/api/v1/users", "/../users", "/pay/test"} {
		if resp, _ := h.do("GET", "/api/subscription"+p, nil, bearer); resp.StatusCode == 200 {
			t.Errorf("relay escaped: %s", p)
		}
	}
	if resp, _ := h.do("GET", "/api/subscription/abcdefghijklmnopqrstuvwx", nil, nil); resp.StatusCode != 401 {
		t.Fatal(resp.StatusCode)
	}
	if resp, _ := h.do("GET", "/api/subscription/abcdefghijklmnopqrstuvwx", nil, bearer); resp.StatusCode != 404 {
		t.Fatal(resp.StatusCode)
	}
	originalTime := h.now
	h.now = h.now.Add(48 * time.Hour)
	if resp, _ := h.do("GET", "/api/subscription/"+u.SubToken, nil, bearer); resp.StatusCode != 401 {
		t.Fatalf("expired key: %d", resp.StatusCode)
	}
	h.now = originalTime
	h.client.Jar = jar
	if resp, _ := h.login(password, ""); resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	if resp, _ := h.do("DELETE", api+"/api-keys/"+strconv.FormatInt(key.ID, 10), nil, csrf); resp.StatusCode != 204 {
		t.Fatal(resp.StatusCode)
	}
	if resp, _ := h.do("GET", "/api/subscription/"+u.SubToken, nil, bearer); resp.StatusCode != 401 {
		t.Fatalf("revoked key: %d", resp.StatusCode)
	}
}

func TestExternalSubscriptionDomain(t *testing.T) {
	h := newHarness(t, func(o *Options) {
		o.DNS = &dnscheck.Checker{Resolvers: []string{"http://127.0.0.1:1"}, HTTP: http.DefaultClient}
	})
	if resp, _ := h.login(password, ""); resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	set := settings.New(h.st.Q)
	for k, v := range map[string]any{settings.KeyPublicHost: "203.0.113.1", settings.KeyPanelPort: 2053} {
		if err := settings.Set(t.Context(), set, k, v); err != nil {
			t.Fatal(err)
		}
	}
	resp, body := h.do("PATCH", "/"+adminPath+"/api/v1/settings", map[string]any{"sub_domain": "edge.example.com", "sub_external": true}, map[string]string{"X-CSRF-Token": h.csrf})
	if resp.StatusCode != 200 || !strings.Contains(string(body), "https://edge.example.com:443/") {
		t.Fatalf("external domain: %d %s", resp.StatusCode, body)
	}
}

func TestExternalDomainMustReturnHereWhenDisabled(t *testing.T) {
	h := newHarness(t, func(o *Options) {
		o.DNS = &dnscheck.Checker{Resolvers: []string{"http://127.0.0.1:1"}, HTTP: http.DefaultClient}
	})
	if resp, _ := h.login(password, ""); resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	set := settings.New(h.st.Q)
	if err := settings.Set(t.Context(), set, settings.KeySubDomain, "elsewhere.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(t.Context(), set, settings.KeySubExternal, true); err != nil {
		t.Fatal(err)
	}
	resp, _ := h.do("PATCH", "/"+adminPath+"/api/v1/settings", map[string]any{"sub_external": false}, map[string]string{"X-CSRF-Token": h.csrf})
	if resp.StatusCode != 422 {
		t.Fatalf("disabled external domain without DNS check: %d", resp.StatusCode)
	}
}

// A database outage must permit edge fallback, not impersonate an invalid/revoked key.
func TestSubscriptionRelayDatabaseOutage(t *testing.T) {
	h := newHarness(t)
	h.st.Close()
	resp, _ := h.do("GET", "/api/subscription/abcdefghijklmnopqrstuvwx", nil, map[string]string{"Authorization": "Bearer mk_" + strings.Repeat("a", 40)})
	if resp.StatusCode != 500 {
		t.Fatalf("database outage returned %d instead of 500", resp.StatusCode)
	}
}
