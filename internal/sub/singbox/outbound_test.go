package singbox

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"mikan/internal/proto"
	"mikan/internal/sub/mihomo"
	"mikan/internal/sub/model"
	"mikan/internal/sub/profile"
	"mikan/internal/sub/routing"
	"mikan/internal/sub/subtest"
	"mikan/internal/sub/subtest/corecheck"
	"mikan/internal/sub/xray"
)

// Shadowsocks wraps UDP in UDP over TCP when the server asks for it, of mihomo's version 1
// when none is given; one of a plugin or smux is left out, as sing-box would dial a server
// that does not answer, and one of a version mihomo refuses too.
func TestShadowsocks(t *testing.T) {
	ss := func(extra map[string]any) map[string]any {
		m := map[string]any{"name": "S", "type": "ss", "server": "203.0.113.7", "port": 8388, "cipher": "aes-128-gcm", "password": "x", "udp": true}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	ss2022 := mihomo.Proxy(model.Proxy{Name: "S", Type: "shadowsocks", Server: "203.0.113.7", Port: 8388,
		Cipher: "2022-blake3-aes-128-gcm", Password: "AAECAwQFBgcICQoLDA0ODw==", UDP: true, UDPOverTCP: 2})
	for _, c := range []struct {
		name string
		in   map[string]any
		ok   bool
		uot  any // the udp_over_tcp block, nil for none
	}{
		{"the panel's 2022", ss2022, true, map[string]any{"enabled": true, "version": 2}},
		{"no version given", ss(map[string]any{"udp-over-tcp": true}), true, map[string]any{"enabled": true, "version": 1}},
		{"version 0", ss(map[string]any{"udp-over-tcp": true, "udp-over-tcp-version": 0}), true, map[string]any{"enabled": true, "version": 1}},
		{"version 3", ss(map[string]any{"udp-over-tcp": true, "udp-over-tcp-version": 3}), false, nil},
		{"version of no number", ss(map[string]any{"udp-over-tcp": true, "udp-over-tcp-version": "two"}), false, nil},
		{"plain", ss(nil), true, nil},
		{"turned off", ss(map[string]any{"udp-over-tcp": false, "udp-over-tcp-version": 2}), true, nil},
		{"obfs", ss(map[string]any{"plugin": "obfs", "plugin-opts": map[string]any{"mode": "tls", "host": "bing.com"}}), false, nil},
		{"shadow-tls", ss(map[string]any{"plugin": "shadow-tls", "plugin-opts": map[string]any{"host": "cloud.tencent.com", "password": "p", "version": 2}}), false, nil},
		{"smux", ss(map[string]any{"smux": map[string]any{"enabled": true, "protocol": "h2mux"}}), false, nil},
		{"smux turned off", ss(map[string]any{"smux": map[string]any{"enabled": false}}), true, nil},
	} {
		o, ok := singboxOutbound(c.in)
		if ok != c.ok {
			t.Errorf("%s: carried %v, want %v: %v", c.name, ok, c.ok, o)
			continue
		}
		if !ok {
			continue
		}
		if o["type"] != "shadowsocks" || !reflect.DeepEqual(o["udp_over_tcp"], c.uot) {
			t.Errorf("%s: %v, want udp_over_tcp %v", c.name, o, c.uot)
		}
	}
}

// A proxy of the admin's own Clash profile that sing-box cannot dial is neither an outbound
// nor a member of the groups that list it; the config sing-box checks has UDP over TCP of
// version 1.
func TestOwnProfileLeavesPluginOut(t *testing.T) {
	lists(t)
	src := `proxies:
  - {name: obfs-ss, type: ss, server: 203.0.113.8, port: 8388, cipher: aes-128-gcm, password: x, plugin: obfs, plugin-opts: {mode: http, host: bing.com}}
  - {name: plain-ss, type: ss, server: 203.0.113.9, port: 8388, cipher: aes-128-gcm, password: x, udp: true, udp-over-tcp: true}
proxy-groups:
  - {name: Сеть, type: select, proxies: [obfs-ss, plain-ss, DIRECT]}
rules:
  - MATCH,Сеть
`
	raw, err := Render(context.Background(), profileOf(subtest.TwoNodes()), model.GroupNames{}, routing.All, subtest.Clash(src))
	if err != nil {
		t.Fatal(err)
	}
	var c sbConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	if plain := outbound(c, "plain-ss"); outbound(c, "obfs-ss") != nil || plain == nil || !reflect.DeepEqual(plain["udp_over_tcp"], map[string]any{"enabled": true, "version": 1.0}) {
		t.Fatalf("outbounds: %v", c.Outbounds)
	}
	for _, o := range c.Outbounds {
		if members, _ := o["outbounds"].([]any); slices.Contains(members, any("obfs-ss")) {
			t.Fatalf("a member sing-box has no outbound for: %v", o)
		}
	}
	corecheck.SingBox(t, raw)
}

// REALITY dials with uTLS, of the panel's fingerprint when the proxy names none.
func TestRealityFingerprint(t *testing.T) {
	o, ok := singboxOutbound(map[string]any{"name": "R", "type": "vless", "server": "203.0.113.7", "port": 443, "uuid": subtest.Keys.UUID,
		"tls": true, "servername": "www.google.com", "reality-opts": map[string]any{"public-key": "k", "short-id": "ab"}})
	if !ok {
		t.Fatal("no outbound")
	}
	if u := o["tls"].(map[string]any)["utls"].(map[string]any); u["fingerprint"] != proto.DefaultFingerprint {
		t.Fatalf("utls: %v", u)
	}
}

// The gRPC keepalive is the one the Clash apps get: sing-box and xray ping after as long an
// idle time as mihomo waits between pings, and give up on an unanswered ping after as long
// as each other (mihomo has no such timeout to set).
func TestGRPCKeepaliveAsMihomo(t *testing.T) {
	p := model.Proxy{Name: "G", Type: "vless", Server: "203.0.113.7", Port: 2053, UUID: subtest.Keys.UUID,
		Transport: &model.Transport{Network: "grpc", Service: "api"}}
	m := mihomo.Proxy(p)
	o, ok := singboxOutbound(m)
	if !ok {
		t.Fatal("no outbound")
	}
	ping := m["grpc-opts"].(map[string]any)["ping-interval"]
	sb := o["transport"].(map[string]any)
	if idle := sb["idle_timeout"]; idle != fmt.Sprintf("%vs", ping) {
		t.Fatalf("sing-box idles %v, mihomo pings every %vs", idle, ping)
	}
	raw, err := xray.Render(profile.Profile{Proxies: []model.Proxy{p}}, model.GroupNames{}, routing.All, model.Template{})
	if err != nil {
		t.Fatal(err)
	}
	var cs []struct{ Outbounds []map[string]any }
	if err := json.Unmarshal(raw, &cs); err != nil {
		t.Fatal(err)
	}
	var x map[string]any
	for _, c := range cs {
		for _, ob := range c.Outbounds {
			if g, ok := anyMap(ob["streamSettings"])["grpcSettings"].(map[string]any); ok {
				x = g
			}
		}
	}
	if x == nil {
		t.Fatalf("no gRPC outbound in the xray profile: %s", raw)
	}
	if idle := fmt.Sprint(x["idle_timeout"]); idle != fmt.Sprint(ping) {
		t.Fatalf("xray idles %vs, mihomo pings every %vs", idle, ping)
	}
	if wait := fmt.Sprintf("%vs", x["health_check_timeout"]); wait != sb["ping_timeout"] {
		t.Fatalf("xray waits %v for an answer, sing-box %v", wait, sb["ping_timeout"])
	}
}
