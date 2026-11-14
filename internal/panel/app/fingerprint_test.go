package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
)

// The admin picks a default TLS fingerprint in the settings and an own one per inbound;
// subscriptions carry them, and values apps would refuse are not accepted.
func TestFingerprintSettingsAndInbound(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := domain.Seed(ctx, h.st, h.now); err != nil {
		t.Fatal(err)
	}
	set := settings.New(h.st.Q)
	for k, v := range map[string]any{settings.KeyPublicHost: "203.0.113.10", settings.KeyPanelPort: 21355} {
		if err := settings.Set(ctx, set, k, v); err != nil {
			t.Fatal(err)
		}
	}
	tariffs, err := h.st.Q.ListTariffs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return h.now }
	u, err := domain.NewUsers(h.st, domain.NewPool(h.st, clock), noChanges{}, clock).Create(ctx, domain.CreateInput{Name: "a", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	api := "/" + adminPath + "/api/v1"

	// Without a session nothing changes.
	if resp, _ := h.do(http.MethodPatch, api+"/settings", map[string]any{"client_fingerprint": "firefox"}, nil); resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusForbidden {
		t.Fatalf("anonymous patch: %d", resp.StatusCode)
	}
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	csrf := map[string]string{"X-CSRF-Token": h.csrf}

	var s struct {
		Fingerprint string `json:"client_fingerprint"`
	}
	resp, body := h.do(http.MethodGet, api+"/settings", nil, nil)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &s) != nil || s.Fingerprint != "chrome" {
		t.Fatalf("default: %d %s", resp.StatusCode, body)
	}
	for _, bad := range []string{"Chrome 120", "", "Chrome", "firefox\n", "a,DIRECT"} {
		if resp, body := h.do(http.MethodPatch, api+"/settings", map[string]any{"client_fingerprint": bad}, csrf); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("%q accepted: %d %s", bad, resp.StatusCode, body)
		}
	}
	// An own value of the right shape is the admin's call.
	if resp, body := h.do(http.MethodPatch, api+"/settings", map[string]any{"client_fingerprint": "chrome120"}, csrf); resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"client_fingerprint":"chrome120"`) {
		t.Fatalf("own value: %d %s", resp.StatusCode, body)
	}
	resp, body = h.do(http.MethodPatch, api+"/settings", map[string]any{"client_fingerprint": "firefox"}, csrf)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &s) != nil || s.Fingerprint != "firefox" {
		t.Fatalf("set: %d %s", resp.StatusCode, body)
	}

	inbounds, err := h.st.Q.ListInbounds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, in := range inbounds {
		ids[in.Preset] = strconv.FormatInt(in.ID, 10)
	}
	xhttp, hy2 := ids["vless_reality_xhttp"], ids["hysteria2"]
	if xhttp == "" || hy2 == "" {
		t.Fatalf("seeded inbounds: %v", ids)
	}
	var view struct {
		Fingerprint *string `json:"fingerprint"`
		Config      string  `json:"config"`
	}
	resp, body = h.do(http.MethodPatch, api+"/inbounds/"+xhttp, map[string]any{"fingerprint": "ios"}, csrf)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &view) != nil || view.Fingerprint == nil || *view.Fingerprint != "ios" {
		t.Fatalf("inbound fingerprint: %d %s", resp.StatusCode, body)
	}
	for _, c := range []struct {
		id   string
		body map[string]any
	}{
		{xhttp, map[string]any{"fingerprint": "Chrome PSK"}},
		{hy2, map[string]any{"fingerprint": "ios"}}, // QUIC: no uTLS
		{xhttp, map[string]any{"config": strings.Replace(view.Config, "fingerprint: ios", "fingerprint: Netscape", 1)}},
	} {
		if resp, body := h.do(http.MethodPatch, api+"/inbounds/"+c.id, c.body, csrf); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("%v accepted: %d %s", c.body, resp.StatusCode, body)
		}
	}
	resp, body = h.do(http.MethodGet, api+"/inbounds", nil, nil)
	var list []struct {
		ID          int64   `json:"id"`
		Fingerprint *string `json:"fingerprint"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &list) != nil {
		t.Fatalf("list: %d %s", resp.StatusCode, body)
	}
	for _, v := range list {
		if strconv.FormatInt(v.ID, 10) == hy2 && v.Fingerprint != nil {
			t.Fatalf("hysteria2 shows a fingerprint: %s", body)
		}
	}

	resp, body = h.do(http.MethodGet, "/"+subPath+"/"+u.SubToken+"?format=uri", nil, map[string]string{"User-Agent": "Happ/3.4.1"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("subscription: %d", resp.StatusCode)
	}
	links, err := base64.StdEncoding.DecodeString(string(body))
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, l := range strings.Split(string(links), "\n") {
		link, err := url.Parse(strings.TrimSpace(l))
		if err != nil || link.Scheme != "vless" {
			continue
		}
		want := "firefox"
		if link.Port() == "443" {
			want = "ios" // the XHTTP preset's own
		}
		if fp := link.Query().Get("fp"); fp != want {
			t.Errorf("%s: fp=%q, want %q", link.Fragment, fp, want)
		}
		seen++
	}
	if seen == 0 {
		t.Fatalf("no vless links: %s", body)
	}
}
