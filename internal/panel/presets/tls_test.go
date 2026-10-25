package presets_test

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"

	"mikan/internal/panel/presets"
	"mikan/internal/proto"
	"mikan/internal/sub/apps"
	builder "mikan/internal/sub/build"
	"mikan/internal/sub/mihomo"
	"mikan/internal/sub/model"
	"mikan/internal/sub/routing"
	"mikan/internal/sub/singbox"
	"mikan/internal/sub/xray"
)

// singboxTLS is the tls of c's outbound in the sing-box profile an app gets.
func singboxTLS(t *testing.T, c model.Proxy) (map[string]any, bool) {
	t.Helper()
	body, err := singbox.Render(context.Background(), mihomo.Profile{Proxies: []model.Proxy{c}}, model.GroupNames{}.WithDefaults(""), routing.DefaultMode, "")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Outbounds []map[string]any `json:"outbounds"`
	}
	if err := json.Unmarshal(body, &cfg); err != nil {
		t.Fatal(err)
	}
	for _, o := range cfg.Outbounds {
		if o["tag"] == c.Name {
			tls, ok := o["tls"].(map[string]any)
			return tls, ok
		}
	}
	return nil, false
}

// VLESS TLS runs on the node certificate: the listener gets the certificate files and no
// REALITY, clients verify the panel's domain like any website, and a self-signed
// certificate is pinned instead. XHTTP stays with the apps that speak it.
func TestVLESSTLSPresets(t *testing.T) {
	cert := proto.Cert{CertPath: "/data/tls/node.crt", KeyPath: "/data/tls/node.key"}
	slot := proto.Slot{Name: "s000001", UUID: "0b4ddc4c-7c4f-4a36-9d62-6f1a44b8c4e1", Secret: "x"}
	for _, id := range []string{presets.PresetTLSXHTTP, presets.PresetTLSVision} {
		src, err := presets.NewConfig(id, "")
		if err != nil {
			t.Fatal(err)
		}
		tpl, err := proto.Parse(src)
		if err != nil {
			t.Fatal(err)
		}
		if tpl["reality-config"] != nil || tpl.Ext().TLS != "node" {
			t.Fatalf("%s: %s", id, src)
		}
		l, err := proto.Listener(tpl, "in", "", "2443", []proto.Slot{slot}, cert, proto.Options{})
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if l["certificate"] != cert.CertPath || l["private-key"] != cert.KeyPath || l["reality-config"] != nil {
			t.Fatalf("%s listener: %v", id, l)
		}

		c, err := builder.Proxy(tpl, builder.Input{Name: "N", Host: "panel.example.com", Port: 2443, PortSpec: "2443", SNI: "panel.example.com", Keys: slot})
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		link := xray.Link(c)
		u, err := url.Parse(link)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		if u.Scheme != "vless" || q.Get("security") != "tls" || q.Get("sni") != "panel.example.com" || q.Get("fp") != proto.DefaultFingerprint ||
			q.Get("pbk") != "" || q.Get("insecure") != "" || q.Get("allowInsecure") != "" || q.Get("pcs") != "" {
			t.Fatalf("%s link: %s", id, link)
		}
		m := mihomo.Proxy(c)
		if m["tls"] != true || m["servername"] != "panel.example.com" || m["reality-opts"] != nil || m["fingerprint"] != nil || m["skip-cert-verify"] != nil {
			t.Fatalf("%s mihomo: %v", id, m)
		}
		needs := proto.NeedsOf(tpl)
		xrayApps, singboxApps := (apps.App{Family: apps.FamilyXray}).Supports(needs), (apps.App{Family: apps.FamilySingBox}).Supports(needs)
		// sing-box apps get the outbound only where they carry its transport.
		if singboxApps {
			if sb, ok := singboxTLS(t, c); !ok || sb["server_name"] != "panel.example.com" || sb["reality"] != nil || sb["insecure"] != nil {
				t.Fatalf("%s sing-box tls: %v", id, sb)
			}
		}
		switch id {
		case presets.PresetTLSXHTTP:
			if q.Get("type") != "xhttp" || q.Get("path") == "" || q.Get("flow") != "" || !xrayApps || singboxApps {
				t.Fatalf("xhttp: %s xray=%v sing-box=%v", link, xrayApps, singboxApps)
			}
		case presets.PresetTLSVision:
			if q.Get("type") != "tcp" || q.Get("flow") != "xtls-rprx-vision" || !xrayApps || !singboxApps {
				t.Fatalf("vision: %s xray=%v sing-box=%v", link, xrayApps, singboxApps)
			}
			if us := l["users"].([]map[string]any); us[0]["flow"] != "xtls-rprx-vision" {
				t.Fatalf("vision users: %v", us)
			}
		}

		// No public certificate: the self-signed one is pinned where each app reads a pin.
		c, err = builder.Proxy(tpl, builder.Input{Name: "N", Host: "203.0.113.7", Port: 2443, PortSpec: "2443", PinSHA256: "ab12", Keys: slot})
		if err != nil {
			t.Fatal(err)
		}
		u, _ = url.Parse(xray.Link(c))
		if u.Query().Get("pcs") != "ab12" || mihomo.Proxy(c)["fingerprint"] != "ab12" {
			t.Fatalf("%s pinned: %s %v", id, xray.Link(c), mihomo.Proxy(c))
		}
	}
}
