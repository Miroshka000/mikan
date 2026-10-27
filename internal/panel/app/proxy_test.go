package app

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
)

// Inbounds behind a TCP proxy (GitHub issue #11): nginx stream on 443 forwards by SNI to
// inbounds on 127.0.0.1. Their ports never move on their own, and subscriptions give
// clients the proxy's address, port and SNI.
func TestInboundsBehindAProxy(t *testing.T) {
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
	tariffs, err := h.st.Q.ListTariffs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return h.now }
	u, err := domain.NewUsers(h.st, domain.NewPool(h.st, clock), noChanges{}, clock).Create(ctx, domain.CreateInput{Name: "a", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	if resp, _ := h.login(password, ""); resp.StatusCode != http.StatusOK {
		t.Fatal("login")
	}
	api := "/" + adminPath + "/api/v1/inbounds"
	csrf := map[string]string{"X-CSRF-Token": h.csrf}
	type view struct {
		ID       int64  `json:"id"`
		Name     string `json:"name"`
		Port     string `json:"port"`
		Listen   string `json:"listen"`
		AutoPort bool   `json:"auto_port"`
		AutoSNI  bool   `json:"auto_sni"`
		Client   struct {
			Server string `json:"server"`
			Port   int    `json:"port"`
			SNI    string `json:"sni"`
		} `json:"client"`
		ClientSNI bool `json:"client_sni"`
	}
	patch := func(id int64, body map[string]any) (int, string, view) {
		t.Helper()
		resp, raw := h.do(http.MethodPatch, api+"/"+strconv.FormatInt(id, 10), body, csrf)
		var v view
		_ = json.Unmarshal(raw, &v)
		return resp.StatusCode, string(raw), v
	}
	ins, err := h.st.Q.ListInbounds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for _, in := range ins {
		ids[in.Name] = in.ID
	}
	xhttp := ids["vless-xhttp"]
	before, _ := h.st.Q.GetInbound(ctx, xhttp)

	// An address of its own turns the port move and the site switch off in the same
	// update: the proxy in front forwards to the port and may route by the site.
	h.advance(time.Hour)
	code, body, v := patch(xhttp, map[string]any{"listen": " 127.0.0.1 "})
	if code != http.StatusOK || v.Listen != "127.0.0.1" || v.AutoPort || v.AutoSNI || v.ClientSNI {
		t.Fatalf("listen: %d %s", code, body)
	}
	if after, _ := h.st.Q.GetInbound(ctx, xhttp); after.Listen != "127.0.0.1" || after.AutoPort != 0 || after.AutoSni != 0 || after.UpdatedAt != before.UpdatedAt {
		t.Fatalf("stored: %+v (clients get nothing new: updated_at stays)", after)
	}
	for _, c := range []struct {
		body      map[string]any
		code      int
		want, loc string
	}{
		{map[string]any{"auto_port": true}, http.StatusUnprocessableEntity, "auto_port_listen", "body.auto_port"},
		{map[string]any{"auto_sni": true}, http.StatusUnprocessableEntity, "auto_sni_listen", "body.auto_sni"},
		{map[string]any{"listen": "localhost"}, http.StatusUnprocessableEntity, "bad_listen", "body.listen"},
		{map[string]any{"listen": "127.0.0.1:444"}, http.StatusUnprocessableEntity, "bad_listen", "body.listen"},
		// REALITY clients send the target's name: not the client side's to change.
		{map[string]any{"client": map[string]any{"server": "proxy.example.net", "port": 443, "sni": "other.example.com"}}, http.StatusUnprocessableEntity, "config_client_sni", "body.client.sni"},
		{map[string]any{"client": map[string]any{"server": "vpn example", "port": 443, "sni": ""}}, http.StatusUnprocessableEntity, "config_client_server", "body.client.server"},
		{map[string]any{"client": map[string]any{"server": "", "port": 70000, "sni": ""}}, http.StatusUnprocessableEntity, "", "body.client.port"},
		// One port number per node and network, whatever the address.
		{map[string]any{"port": "443"}, http.StatusConflict, "port_in_use", "body.port"},
	} {
		id := xhttp
		if c.code == http.StatusConflict {
			id = ids["vless-vision"]
		}
		if code, body, _ := patch(id, c.body); code != c.code || !strings.Contains(body, c.want) || !strings.Contains(body, c.loc) {
			t.Errorf("%v: %d %s", c.body, code, body)
		}
	}
	code, body, v = patch(xhttp, map[string]any{"client": map[string]any{"server": "proxy.example.net", "port": 443, "sni": ""}})
	if code != http.StatusOK || v.Client.Server != "proxy.example.net" || v.Client.Port != 443 || v.Listen != "127.0.0.1" || v.AutoPort {
		t.Fatalf("client endpoint: %d %s", code, body)
	}

	// AnyTLS on the node certificate: nginx tells it apart by its own name.
	resp, raw := h.do(http.MethodPost, api, map[string]any{"preset": "anytls", "port": "8445"}, csrf)
	var anytls view
	if resp.StatusCode != http.StatusCreated || json.Unmarshal(raw, &anytls) != nil || !anytls.ClientSNI || anytls.Listen != "" {
		t.Fatalf("anytls: %d %s", resp.StatusCode, raw)
	}
	code, body, v = patch(anytls.ID, map[string]any{"listen": "127.0.0.1", "auto_port": false,
		"client": map[string]any{"server": "proxy.example.net", "port": 443, "sni": "anytls.example.com"}})
	if code != http.StatusOK || v.Listen != "127.0.0.1" || v.Client.SNI != "anytls.example.com" || v.Port != "8445" {
		t.Fatalf("anytls behind the proxy: %d %s", code, body)
	}

	// Share links and the Clash profile: the proxy's endpoint, the usual REALITY name.
	// Xray apps (Happ) get no AnyTLS: its link is checked in the profile.
	resp, raw = h.do(http.MethodGet, "/"+subPath+"/"+u.SubToken+"?format=uri", nil, map[string]string{"User-Agent": "Happ/3.4.1"})
	links, err := base64.StdEncoding.DecodeString(string(raw))
	if resp.StatusCode != http.StatusOK || err != nil {
		t.Fatalf("subscription: %d %v", resp.StatusCode, err)
	}
	seen := map[string]bool{}
	for _, l := range strings.Split(string(links), "\n") {
		link, err := url.Parse(strings.TrimSpace(l))
		if err != nil || link.Scheme != "vless" {
			continue
		}
		q := link.Query()
		switch {
		case q.Get("type") == "xhttp":
			seen["xhttp"] = link.Host == "proxy.example.net:443" && q.Get("sni") == "www.microsoft.com"
		default: // Vision listens on every address as before
			seen["vision"] = link.Host == "vpn.example.com:8443"
		}
	}
	if !seen["xhttp"] || !seen["vision"] {
		t.Fatalf("links %v:\n%s", seen, links)
	}
	resp, raw = h.do(http.MethodGet, "/"+subPath+"/"+u.SubToken, nil, map[string]string{"User-Agent": "mihomo/1.19.31"})
	var prof struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if resp.StatusCode != http.StatusOK || yaml.Unmarshal(raw, &prof) != nil {
		t.Fatalf("profile: %d %s", resp.StatusCode, raw)
	}
	found := 0
	for _, p := range prof.Proxies {
		switch p["type"] {
		case "anytls":
			found++
			if p["server"] != "proxy.example.net" || p["port"] != 443 || p["sni"] != "anytls.example.com" {
				t.Errorf("anytls proxy: %v", p)
			}
		case "vless":
			if p["network"] == "xhttp" {
				found++
				if p["server"] != "proxy.example.net" || p["port"] != 443 || p["servername"] != "www.microsoft.com" {
					t.Errorf("xhttp proxy: %v", p)
				}
			}
		}
	}
	if found != 2 {
		t.Fatalf("profile proxies: %s", raw)
	}

	// Back on every address the port move may be switched on again; the override stays.
	if code, body, v = patch(xhttp, map[string]any{"listen": ""}); code != http.StatusOK || v.Listen != "" || v.AutoPort || v.Client.Server != "proxy.example.net" {
		t.Fatalf("every address: %d %s", code, body)
	}
	if code, body, v = patch(xhttp, map[string]any{"auto_port": true}); code != http.StatusOK || !v.AutoPort {
		t.Fatalf("auto_port again: %d %s", code, body)
	}
	if code, body, v = patch(xhttp, map[string]any{"client": map[string]any{"server": "", "port": 0, "sni": ""}}); code != http.StatusOK || v.Client.Server != "" || v.Client.Port != 0 {
		t.Fatalf("clear the endpoint: %d %s", code, body)
	}
}
