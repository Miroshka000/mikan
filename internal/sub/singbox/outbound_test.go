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

// carried is a proxy of an own Clash profile as sing-box gets it: read into the model, then
// written as an outbound.
func carried(m map[string]any) (map[string]any, bool) {
	p, ok := mihomo.ReadProxy(m)
	if !ok {
		return nil, false
	}
	return singboxOutbound(p)
}

// Each protocol sing-box speaks is its outbound; what it cannot carry is none.
func TestOutbound(t *testing.T) {
	const host = `"server":"203.0.113.7","server_port":443`
	for _, c := range []struct {
		name string
		in   model.Proxy
		want string // the outbound's JSON, "" for none
	}{
		{"VLESS REALITY", model.Proxy{Name: "V", Type: "vless", Server: "203.0.113.7", Port: 443, UUID: "u", Flow: "xtls-rprx-vision", UDP: true,
			TLS:       &model.TLS{SNI: "www.google.com", Fingerprint: "chrome", Reality: &model.Reality{PublicKey: "k", ShortID: "ab", MLKEM: true}},
			Transport: &model.Transport{Network: "tcp"}},
			`{"flow":"xtls-rprx-vision","packet_encoding":"xudp",` + host + `,"tag":"V","tls":{"enabled":true,"reality":{"enabled":true,"public_key":"k","short_id":"ab"},"server_name":"www.google.com","utls":{"enabled":true,"fingerprint":"chrome"}},"type":"vless","uuid":"u"}`},
		{"VLESS gRPC without TLS", model.Proxy{Name: "V", Type: "vless", Server: "203.0.113.7", Port: 443, UUID: "u", UDP: true,
			Transport: &model.Transport{Network: "grpc", Service: "api"}},
			`{"packet_encoding":"xudp",` + host + `,"tag":"V","transport":{"idle_timeout":"15s","ping_timeout":"10s","service_name":"api","type":"grpc"},"type":"vless","uuid":"u"}`},
		{"Trojan WebSocket of no UDP", model.Proxy{Name: "T", Type: "trojan", Server: "203.0.113.7", Port: 443, Password: "p",
			TLS:       &model.TLS{SNI: "a.example", Insecure: true},
			Transport: &model.Transport{Network: "ws", Path: "/w", Host: "cdn.example", Headers: map[string]string{"User-Agent": "x"}}},
			`{"network":"tcp","password":"p",` + host + `,"tag":"T","tls":{"enabled":true,"insecure":true,"server_name":"a.example"},"transport":{"headers":{"Host":"cdn.example","User-Agent":"x"},"path":"/w","type":"ws"},"type":"trojan"}`},
		{"Hysteria2", model.Proxy{Name: "H", Type: "hysteria2", Server: "203.0.113.7", Port: 443, Ports: "20000-30000, 40000", Password: "p", ALPN: []string{"h3"},
			Obfs: &model.Obfs{Type: "salamander", Password: "o"}, TLS: &model.TLS{SNI: "a.example", Fingerprint: "chrome"}},
			`{"obfs":{"password":"o","type":"salamander"},"password":"p","server":"203.0.113.7","server_ports":["20000:30000","40000"],"tag":"H","tls":{"alpn":["h3"],"enabled":true,"server_name":"a.example"},"type":"hysteria2"}`},
		{"TUIC", model.Proxy{Name: "U", Type: "tuic", Server: "203.0.113.7", Port: 443, UUID: "u", Password: "p", ALPN: []string{"h3"}, Congestion: "bbr"},
			`{"congestion_control":"bbr","password":"p",` + host + `,"tag":"U","tls":{"alpn":["h3"],"enabled":true},"type":"tuic","udp_relay_mode":"native","uuid":"u"}`},
		{"TUIC of QUIC relaying", model.Proxy{Name: "U", Type: "tuic", Server: "203.0.113.7", Port: 443, UUID: "u", Password: "p", Congestion: "bbr", UDPRelay: "quic"},
			`{"congestion_control":"bbr","password":"p",` + host + `,"tag":"U","tls":{"enabled":true},"type":"tuic","udp_relay_mode":"quic","uuid":"u"}`},
		{"AnyTLS", model.Proxy{Name: "A", Type: "anytls", Server: "203.0.113.7", Port: 443, Password: "p", UDP: true, TLS: &model.TLS{SNI: "a.example", Fingerprint: "chrome"}},
			`{"password":"p",` + host + `,"tag":"A","tls":{"enabled":true,"server_name":"a.example","utls":{"enabled":true,"fingerprint":"chrome"}},"type":"anytls"}`},
		{"Shadowsocks", model.Proxy{Name: "S", Type: "shadowsocks", Server: "203.0.113.7", Port: 443, Cipher: "2022-blake3-aes-128-gcm", Password: "k", UDP: true, UDPOverTCP: 2},
			`{"method":"2022-blake3-aes-128-gcm","password":"k",` + host + `,"tag":"S","type":"shadowsocks","udp_over_tcp":{"enabled":true,"version":2}}`},
		{"VLESS Encryption", model.Proxy{Name: "V", Type: "vless", Server: "203.0.113.7", Port: 443, UUID: "u", Encryption: "mlkem768x25519plus.native.0rtt.k"}, ""},
		{"XHTTP", model.Proxy{Name: "V", Type: "vless", Server: "203.0.113.7", Port: 443, UUID: "u", TLS: &model.TLS{},
			Transport: &model.Transport{Network: "xhttp", XHTTP: &model.XHTTP{}}}, ""},
		{"a pinned certificate", model.Proxy{Name: "T", Type: "trojan", Server: "203.0.113.7", Port: 443, Password: "p", TLS: &model.TLS{Pin: "ab"}}, ""},
		{"Gecko", model.Proxy{Name: "H", Type: "hysteria2", Server: "203.0.113.7", Port: 443, Password: "p", Obfs: &model.Obfs{Type: "gecko", Password: "o"}}, ""},
		{"Mieru", model.Proxy{Name: "M", Type: "mieru", Server: "203.0.113.7", Port: 443, Username: "u", Password: "p"}, ""},
		{"Snell", model.Proxy{Name: "N", Type: "snell", Server: "203.0.113.7", Port: 443, Snell: &model.Snell{PSK: "k", Version: 3}}, ""},
	} {
		o, ok := singboxOutbound(c.in)
		if ok != (c.want != "") {
			t.Errorf("%s: carried %v: %v", c.name, ok, o)
			continue
		}
		if raw, _ := json.Marshal(o); ok && string(raw) != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, raw, c.want)
		}
	}
}

// A proxy of the admin's own Clash profile stays in the config sing-box gets: an outbound,
// a member of its group and the entry of its chain, the panel's servers dialing through it.
func TestOwnProfileProxies(t *testing.T) {
	lists(t)
	src := `proxies:
  - {name: вход, type: trojan, server: 198.51.100.7, port: 443, password: x, sni: entry.example, tls: true, skip-cert-verify: true, udp: true}
  - {name: прыжки, type: hysteria2, server: 198.51.100.8, ports: 20000-30000, password: x}
  - {name: cdn, type: vless, server: 198.51.100.9, port: 443, uuid: 0b4ddc4c-7c4f-4a36-9d62-6f1a44b8c4e1, tls: true, network: ws, ws-opts: {path: /w, headers: {Host: cdn.example, User-Agent: x}}}
proxy-groups:
  - {name: Сеть, type: select, proxies: [Цепь, вход, прыжки, cdn, DIRECT]}
  - {name: Серверы, type: url-test, include-all-proxies: true}
  - {name: Цепь, type: chain, proxies: [вход, Серверы]}
rules:
  - MATCH,Сеть
`
	prof := profileOf(subtest.TwoNodes())
	raw, err := Render(context.Background(), prof, model.GroupNames{}, routing.All, subtest.Clash(src))
	if err != nil {
		t.Fatal(err)
	}
	var c sbConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	entry := outbound(c, "вход")
	if tls, _ := entry["tls"].(map[string]any); entry["type"] != "trojan" || entry["server"] != "198.51.100.7" ||
		tls["server_name"] != "entry.example" || tls["insecure"] != true {
		t.Fatalf("the template's own proxy: %v", entry)
	}
	if h := outbound(c, "прыжки"); h == nil || !reflect.DeepEqual(h["server_ports"], []any{"20000:30000"}) {
		t.Fatalf("a Hysteria2 of a range alone: %v", h)
	}
	if w := outbound(c, "cdn"); w == nil || !reflect.DeepEqual(anyMap(w["transport"])["headers"], map[string]any{"Host": "cdn.example", "User-Agent": "x"}) {
		t.Fatalf("a WebSocket of its own headers: %v", w)
	}
	for _, m := range []string{"вход", "прыжки", "cdn"} {
		if !slices.Contains(anyList(outbound(c, "Сеть")["outbounds"]), any(m)) {
			t.Fatalf("%s is not in the group: %v", m, outbound(c, "Сеть"))
		}
	}
	dialed := 0
	for _, p := range prof.Proxies {
		if o := outbound(c, p.Name); o != nil {
			if dialed++; o["detour"] != "вход" {
				t.Errorf("%s dials past the chain's entry: %v", p.Name, o)
			}
		}
	}
	if dialed == 0 {
		t.Fatalf("no server of the panel's: %v", c.Outbounds)
	}
	corecheck.SingBox(t, raw)
}

// The chains of each shape Citrus loads, as Citrus wires them: every server's own outbound
// dials through the hop before it, of a chain as a hop its first hop, of a group as a hop its
// members, and past DIRECT nothing. sing-box runs it.
func TestChainShapes(t *testing.T) {
	lists(t)
	// sing-box carries no XHTTP: the template's own proxies.
	src := `proxies:
  - {name: a, type: trojan, server: 198.51.100.7, port: 443, password: x, sni: a.example, udp: true}
  - {name: b, type: ss, server: 198.51.100.8, port: 8388, cipher: aes-128-gcm, password: x}
  - {name: c, type: trojan, server: 198.51.100.9, port: 443, password: x, sni: c.example}
  - {name: d, type: ss, server: 198.51.100.10, port: 8388, cipher: aes-128-gcm, password: x}
` + subtest.Chains("a", "b", "c", "d")
	prof := profileOf(subtest.TwoNodes())
	if err := mihomo.CheckTemplate(prof, src); err != nil {
		t.Fatalf("the template is not saved: %v", err)
	}
	raw, err := Render(context.Background(), prof, model.GroupNames{}, routing.All, subtest.Clash(src))
	if err != nil {
		t.Fatal(err)
	}
	var c sbConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]any{"b": "a", "a": "c", "d": "b", "c": nil} {
		if o := outbound(c, name); o == nil || o["detour"] != want {
			t.Errorf("%s dials through %v, want %v", name, o["detour"], want)
		}
	}
	corecheck.SingBox(t, raw)
}

// Shadowsocks wraps UDP in UDP over TCP when the server asks for it, of mihomo's version 1
// when none is given; one of a version mihomo refuses is left out.
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
	} {
		o, ok := carried(c.in)
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

// A Shadowsocks plugin sing-box runs too (obfs, v2ray-plugin) and smux are the outbound's
// plugin and multiplex; a plugin sing-box has none of, or a setting of one it cannot take,
// leaves the proxy out, as sing-box would dial a server that does not answer.
func TestPluginsAndMux(t *testing.T) {
	ss := func(extra map[string]any) map[string]any {
		m := map[string]any{"name": "S", "type": "ss", "server": "203.0.113.7", "port": 8388, "cipher": "aes-128-gcm", "password": "x", "udp": true}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	v2ray := func(opts map[string]any) map[string]any {
		o := map[string]any{"mode": "websocket"}
		for k, v := range opts {
			o[k] = v
		}
		return ss(map[string]any{"plugin": "v2ray-plugin", "plugin-opts": o})
	}
	trojan := func(smux map[string]any) map[string]any {
		return map[string]any{"name": "T", "type": "trojan", "server": "203.0.113.7", "port": 443, "password": "p", "sni": "a.example", "smux": smux}
	}
	for _, c := range []struct {
		name string
		in   map[string]any
		want string // the outbound's plugin, plugin_opts and multiplex as JSON, "" for no outbound
	}{
		{"obfs", ss(map[string]any{"plugin": "obfs", "plugin-opts": map[string]any{"mode": "tls", "host": "a.example"}}),
			`{"plugin":"obfs-local","plugin_opts":"obfs=tls;obfs-host=a.example"}`},
		{"obfs of mihomo's host", ss(map[string]any{"plugin": "obfs", "plugin-opts": map[string]any{"mode": "http"}}),
			`{"plugin":"obfs-local","plugin_opts":"obfs=http;obfs-host=bing.com"}`},
		{"obfs of no mode", ss(map[string]any{"plugin": "obfs", "plugin-opts": map[string]any{"host": "a.example"}}), ""},
		{"v2ray-plugin", v2ray(map[string]any{"tls": true, "host": "a.example", "path": `/w;x=1\`, "mux": true, "skip-cert-verify": false, "headers": map[string]any{}}),
			`{"plugin":"v2ray-plugin","plugin_opts":"mode=websocket;tls;host=a.example;path=/w\\;x\\=1\\\\"}`},
		{"v2ray-plugin of no mux", v2ray(map[string]any{"mux": false}),
			`{"plugin":"v2ray-plugin","plugin_opts":"mode=websocket;host=bing.com;mux=0"}`},
		{"v2ray-plugin of ECH turned off", v2ray(map[string]any{"ech-opts": map[string]any{"enabled": false}}),
			`{"plugin":"v2ray-plugin","plugin_opts":"mode=websocket;host=bing.com"}`},
		{"v2ray-plugin of skip-cert-verify", v2ray(map[string]any{"tls": true, "skip-cert-verify": true}), ""},
		{"v2ray-plugin of headers", v2ray(map[string]any{"headers": map[string]any{"User-Agent": "x"}}), ""},
		{"v2ray-plugin of a pinned certificate", v2ray(map[string]any{"tls": true, "fingerprint": "ab"}), ""},
		{"v2ray-plugin of HTTPUpgrade", v2ray(map[string]any{"v2ray-http-upgrade": true}), ""},
		{"v2ray-plugin over QUIC", v2ray(map[string]any{"mode": "quic"}), ""},
		{"shadow-tls", ss(map[string]any{"plugin": "shadow-tls", "plugin-opts": map[string]any{"host": "cloud.tencent.com", "password": "p", "version": 2}}), ""},
		{"gost-plugin", ss(map[string]any{"plugin": "gost-plugin", "plugin-opts": map[string]any{"mode": "websocket"}}), ""},
		{"smux", ss(map[string]any{"smux": map[string]any{"enabled": true}}), `{"multiplex":{"enabled":true}}`},
		{"smux beside UDP over TCP", ss(map[string]any{"udp-over-tcp": true, "smux": map[string]any{"enabled": true, "protocol": "smux"}}), `{}`},
		{"smux turned off", ss(map[string]any{"smux": map[string]any{"enabled": false, "protocol": "smux"}}), `{}`},
		{"smux of Trojan", trojan(map[string]any{"enabled": true, "protocol": "yamux", "max-connections": 4, "min-streams": 2, "padding": true, "statistic": true}),
			`{"multiplex":{"enabled":true,"max_connections":4,"min_streams":2,"padding":true,"protocol":"yamux"}}`},
		{"smux of Brutal", trojan(map[string]any{"enabled": true, "protocol": "h2mux", "max-streams": 8,
			"brutal-opts": map[string]any{"enabled": true, "up": 50, "down": "100 Mbps"}}),
			`{"multiplex":{"brutal":{"down_mbps":100,"enabled":true,"up_mbps":50},"enabled":true,"max_streams":8,"protocol":"h2mux"}}`},
		{"smux of Brutal in MB/s", trojan(map[string]any{"enabled": true, "brutal-opts": map[string]any{"enabled": true, "up": "10 MB/s", "down": 100}}), ""},
		{"smux of TCP alone", trojan(map[string]any{"enabled": true, "only-tcp": true}), ""},
		{"smux of an unknown protocol", trojan(map[string]any{"enabled": true, "protocol": "mux.cool"}), ""},
		{"smux of AnyTLS", map[string]any{"name": "A", "type": "anytls", "server": "203.0.113.7", "port": 443, "password": "p",
			"smux": map[string]any{"enabled": true}}, ""},
	} {
		o, ok := carried(c.in)
		if ok != (c.want != "") {
			t.Errorf("%s: carried %v: %v", c.name, ok, o)
			continue
		}
		if !ok {
			continue
		}
		got := map[string]any{}
		for _, k := range []string{"plugin", "plugin_opts", "multiplex"} {
			if v, ok := o[k]; ok {
				got[k] = v
			}
		}
		if raw, _ := json.Marshal(got); string(raw) != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, raw, c.want)
		}
	}
}

// A proxy of the admin's own Clash profile that sing-box cannot dial is neither an outbound
// nor a member of the groups that list it, and the preview tells it; those of a plugin or
// smux it runs too are outbounds. The config sing-box checks has UDP over TCP of version 1,
// both plugins and every multiplex setting.
func TestOwnProfileLeavesPluginOut(t *testing.T) {
	lists(t)
	src := `proxies:
  - {name: tls-ss, type: ss, server: 203.0.113.8, port: 8388, cipher: aes-128-gcm, password: x, plugin: shadow-tls, plugin-opts: {host: cloud.tencent.com, password: p, version: 2}}
  - {name: plain-ss, type: ss, server: 203.0.113.9, port: 8388, cipher: aes-128-gcm, password: x, udp: true, udp-over-tcp: true}
  - {name: obfs-ss, type: ss, server: 203.0.113.10, port: 8388, cipher: aes-128-gcm, password: x, plugin: obfs, plugin-opts: {mode: tls, host: a.example}}
  - {name: v2ray-ss, type: ss, server: 203.0.113.11, port: 443, cipher: aes-128-gcm, password: x, plugin: v2ray-plugin, plugin-opts: {mode: websocket, tls: true, host: a.example, path: /w, mux: false}}
  - {name: mux-trojan, type: trojan, server: 203.0.113.12, port: 443, password: x, sni: a.example, smux: {enabled: true, protocol: smux, max-connections: 4, min-streams: 4, padding: true, brutal-opts: {enabled: true, up: 50, down: 100}}}
  - {name: mux-vless, type: vless, server: 203.0.113.13, port: 443, uuid: 0b4ddc4c-7c4f-4a36-9d62-6f1a44b8c4e1, tls: true, servername: a.example, network: ws, ws-opts: {path: /w}, smux: {enabled: true, protocol: yamux, max-streams: 8}}
proxy-groups:
  - {name: Сеть, type: select, proxies: [tls-ss, plain-ss, obfs-ss, v2ray-ss, mux-trojan, mux-vless, DIRECT]}
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
	if plain := outbound(c, "plain-ss"); outbound(c, "tls-ss") != nil || plain == nil || !reflect.DeepEqual(plain["udp_over_tcp"], map[string]any{"enabled": true, "version": 1.0}) {
		t.Fatalf("outbounds: %v", c.Outbounds)
	}
	for _, name := range []string{"obfs-ss", "v2ray-ss", "mux-trojan", "mux-vless"} {
		if o := outbound(c, name); o == nil || o["plugin"] == nil && o["multiplex"] == nil {
			t.Fatalf("%s has no plugin or multiplex: %v", name, o)
		}
	}
	// The preview tells it, and none of the template's own that went in.
	var told []string
	for _, n := range Dropped(profileOf(subtest.TwoNodes()), model.GroupNames{}, routing.All, subtest.Clash(src)) {
		if n.Code == "proxy_unsupported" {
			told = append(told, n.Subject)
		}
	}
	if !slices.Contains(told, "tls-ss") {
		t.Fatalf("the preview does not tell the left out proxy: %v", told)
	}
	for _, name := range []string{"obfs-ss", "v2ray-ss", "mux-trojan", "mux-vless"} {
		if slices.Contains(told, name) {
			t.Fatalf("the preview tells %s as left out: %v", name, told)
		}
	}
	for _, o := range c.Outbounds {
		if members, _ := o["outbounds"].([]any); slices.Contains(members, any("tls-ss")) {
			t.Fatalf("a member sing-box has no outbound for: %v", o)
		}
	}
	corecheck.SingBox(t, raw)
}

// REALITY dials with uTLS, of the panel's fingerprint when the proxy names none.
func TestRealityFingerprint(t *testing.T) {
	o, ok := carried(map[string]any{"name": "R", "type": "vless", "server": "203.0.113.7", "port": 443, "uuid": subtest.Keys.UUID,
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
	o, ok := carried(m)
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
