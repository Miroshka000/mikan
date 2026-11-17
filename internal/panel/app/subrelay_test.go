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
	"mikan/internal/sub/apps"
	"mikan/internal/subrelay"
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
	if resp, _ := h.do("GET", "/"+adminPath+"/api/subscription/abcdefghijklmnopqrstuvwx", nil, map[string]string{"Authorization": "Bearer " + full.Key}); resp.StatusCode != 404 {
		t.Fatalf("general key reached relay: %d", resp.StatusCode)
	}
	if resp, _ := h.do("GET", "/"+adminPath+"/api/subscription/abcdefghijklmnopqrstuvwx", nil, nil); resp.StatusCode != 404 {
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
	profileResp, profileBody := h.do("GET", "/"+adminPath+"/api/subscription/"+u.SubToken, nil, bearer)
	if profileResp.StatusCode != 200 || len(profileBody) == 0 {
		t.Fatalf("profile: %d %s", profileResp.StatusCode, profileBody)
	}
	proof := profileResp.Header.Get(subrelay.RelayProofHeader)
	if len(proof) != 43 {
		t.Fatal("authenticated edge did not receive its diagnostic ticket")
	}
	foreign := map[string]string{"Authorization": "Bearer " + full.Key, subrelay.RelayProofHeader: proof, subrelay.RelayNonceHeader: "nonce"}
	if resp, _ := h.do("HEAD", "/"+adminPath+"/api/subscription"+subrelay.AuthPath, nil, foreign); resp.StatusCode != 404 || resp.Header.Get(subrelay.RelayAuthHeader) != "" {
		t.Fatal("subscription ticket authenticated another API key")
	}

	// The dev response-rule drop is transported as intent, never as an origin outage.
	h.client.Jar = jar
	rule := apps.Rule{Name: "edge drop", Enabled: true, When: apps.Group{Op: apps.And, Conds: []apps.Cond{{Header: "X-Edge-Rule", Op: apps.OpEquals, Value: "drop"}}, Groups: []apps.Inner{}}, Do: apps.Action{Respond: apps.RespondDrop}}
	if resp, body := h.do("PUT", api+"/sub-rules", map[string]any{"builtin": true, "rules": []apps.Rule{rule}}, map[string]string{"X-CSRF-Token": h.csrf}); resp.StatusCode != 200 {
		t.Fatalf("save drop rule: %d %s", resp.StatusCode, body)
	}
	h.client.Jar = nil
	bearer["X-Edge-Rule"] = "drop"
	if resp, _ := h.do("GET", "/"+adminPath+"/api/subscription/"+u.SubToken, nil, bearer); resp.StatusCode != 204 || resp.Header.Get(subrelay.RelayActionHeader) != "drop" {
		t.Fatalf("rule drop became an outage: %d %v", resp.StatusCode, resp.Header)
	}
	delete(bearer, "X-Edge-Rule")
	// Unknown paths and unknown subscriber tokens are refused.
	for _, p := range []string{"/admin/api/v1/users", "/../users", "/pay/test"} {
		if resp, _ := h.do("GET", "/"+adminPath+"/api/subscription"+p, nil, bearer); resp.StatusCode == 200 {
			t.Errorf("relay escaped: %s", p)
		}
	}
	if resp, _ := h.do("GET", "/"+adminPath+"/api/subscription/abcdefghijklmnopqrstuvwx", nil, nil); resp.StatusCode != 404 {
		t.Fatal(resp.StatusCode)
	}
	if resp, _ := h.do("GET", "/"+adminPath+"/api/subscription/abcdefghijklmnopqrstuvwx", nil, bearer); resp.StatusCode != 404 {
		t.Fatal(resp.StatusCode)
	}
	originalTime := h.now
	h.setNow(h.now.Add(48 * time.Hour))
	if resp, _ := h.do("GET", "/"+adminPath+"/api/subscription/"+u.SubToken, nil, bearer); resp.StatusCode != 404 {
		t.Fatalf("expired key: %d", resp.StatusCode)
	}
	h.setNow(originalTime)
	h.client.Jar = jar
	if resp, _ := h.login(password, ""); resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	if resp, _ := h.do("DELETE", api+"/api-keys/"+strconv.FormatInt(key.ID, 10), nil, csrf); resp.StatusCode != 204 {
		t.Fatal(resp.StatusCode)
	}
	for _, ticket := range []string{"", strings.Repeat("x", 43), proof} {
		headers := map[string]string{"Authorization": "Bearer " + key.Key, subrelay.RelayProofHeader: ticket, subrelay.RelayNonceHeader: "nonce"}
		// The ticket cannot access data or alter ordinary invalid-key fallback responses.
		resp, _ := h.do("GET", "/"+adminPath+"/api/subscription/"+u.SubToken, nil, headers)
		if resp.StatusCode != 404 || resp.Header.Get(subrelay.RelayAuthHeader) != "" {
			t.Fatal("ticket bypassed subscription authorization")
		}
		resp, _ = h.do("HEAD", "/"+adminPath+"/api/subscription"+subrelay.AuthPath, nil, headers)
		if ticket == proof {
			if resp.StatusCode != 204 || resp.Header.Get(subrelay.RelayAuthHeader) != "revoked:nonce" {
				t.Fatal("known edge did not receive a private revocation signal")
			}
		} else if resp.StatusCode != 404 || resp.Header.Get(subrelay.RelayAuthHeader) != "" {
			t.Fatal("unknown key/ticket fingerprinted relay")
		}
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
	for k, v := range map[string]any{settings.KeyPublicHost: "203.0.113.1", settings.KeyPanelPort: 2053, settings.KeySubPort: 2054} {
		if err := settings.Set(t.Context(), set, k, v); err != nil {
			t.Fatal(err)
		}
	}
	resp, body := h.do("PATCH", "/"+adminPath+"/api/v1/settings", map[string]any{"sub_domain": "edge.example.com", "sub_external": true}, map[string]string{"X-CSRF-Token": h.csrf})
	if resp.StatusCode != 200 || !strings.Contains(string(body), "https://edge.example.com:443/") {
		t.Fatalf("external domain: %d %s", resp.StatusCode, body)
	}
	ep, err := set.PanelSubEndpoint(t.Context())
	if err != nil || ep.Host != "203.0.113.1" || ep.Port != 2054 {
		t.Fatalf("payment listener followed edge: %+v %v", ep, err)
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
	resp, _ := h.do("GET", "/"+adminPath+"/api/subscription/abcdefghijklmnopqrstuvwx", nil, map[string]string{"Authorization": "Bearer mk_" + strings.Repeat("a", 40)})
	if resp.StatusCode != 503 {
		t.Fatalf("database outage returned %d instead of 503", resp.StatusCode)
	}
}

func TestSubscriptionRelayUnauthorizedHasOrdinaryFallback(t *testing.T) {
	for _, ownSite := range []bool{false, true} {
		t.Run(strconv.FormatBool(ownSite), func(t *testing.T) {
			h := newHarness(t)
			if ownSite {
				h.p.server.SetSite(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(418); w.Write([]byte("ordinary own site")) }))
			}
			if resp, _ := h.login(password, ""); resp.StatusCode != 200 {
				t.Fatal(resp.StatusCode)
			}
			api := "/" + adminPath + "/api/v1"
			create := func(scope string) string {
				resp, body := h.do("POST", api+"/api-keys", map[string]any{"name": scope, "scope": scope, "password": password}, map[string]string{"X-CSRF-Token": h.csrf})
				var key struct {
					Key string `json:"key"`
				}
				if resp.StatusCode != 201 || json.Unmarshal(body, &key) != nil {
					t.Fatalf("create %s: %d %s", scope, resp.StatusCode, body)
				}
				return key.Key
			}
			subscription, full := create("subscription"), create("full")
			baseline, body := h.do("GET", "/unknown-path", nil, nil)
			for _, token := range []string{"", "mk_" + strings.Repeat("z", 40), full} {
				headers := map[string]string{"Authorization": "Bearer " + token, subrelay.RelayNonceHeader: "untrusted challenge"}
				for _, suffix := range []string{"/abcdefghijklmnopqrstuvwx", subrelay.AuthPath} {
					resp, got := h.do("GET", "/"+adminPath+"/api/subscription"+suffix, nil, headers)
					if resp.StatusCode != baseline.StatusCode || string(got) != string(body) {
						t.Errorf("invalid credentials changed fallback: %d %s", resp.StatusCode, got)
					}
					for name := range resp.Header {
						if strings.HasPrefix(strings.ToLower(name), "x-mikan-relay-") {
							t.Errorf("invalid credentials exposed %s", name)
						}
					}
				}
			}
			headers := map[string]string{"Authorization": "Bearer " + subscription, subrelay.RelayNonceHeader: "private challenge"}
			resp, _ := h.do("HEAD", "/"+adminPath+"/api/subscription"+subrelay.AuthPath, nil, headers)
			if resp.StatusCode != 204 || resp.Header.Get(subrelay.RelayAuthHeader) != "private challenge" {
				t.Fatalf("authenticated probe failed: %d %v", resp.StatusCode, resp.Header)
			}
		})
	}
}
