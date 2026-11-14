package mihomo

import (
	"reflect"
	"testing"

	"mikan/internal/sub/model"
	"mikan/internal/sub/subtest"
)

// Every proxy the panel makes reads back as itself from what Proxy writes of it, but for
// what the model cannot hold of a template: XHTTP and the protocols of mihomo's own.
func TestReadProxyRoundTrip(t *testing.T) {
	users := []subtest.User{subtest.EveryType()}
	for _, v := range subtest.CoreVariants {
		users = append(users, v.User())
	}
	read := map[string]bool{}
	for _, u := range users {
		for _, p := range u.Proxies() {
			got, ok := ReadProxy(Proxy(p))
			want := !(p.Transport != nil && p.Transport.Network == "xhttp") &&
				p.Type != "trusttunnel" && p.Type != "shadowquic" && p.Type != "mieru" && p.Type != "sudoku" && p.Type != "snell"
			if ok != want {
				t.Errorf("%s: read %v, want %v", p.Name, ok, want)
				continue
			}
			if !ok {
				continue
			}
			read[p.Type] = true
			// Where the proxy is from is the panel's, not the profile's.
			p.Node, p.Pool, p.NoAuto = 0, 0, false
			if len(p.ALPN) == 0 {
				p.ALPN = nil
			}
			if !reflect.DeepEqual(got, p) {
				t.Errorf("%s:\n got %+v\nwant %+v", p.Name, got, p)
			}
		}
	}
	for _, typ := range []string{"vless", "trojan", "hysteria2", "tuic", "anytls", "shadowsocks"} {
		if !read[typ] {
			t.Errorf("no %s proxy read back", typ)
		}
	}
	// What a template's own proxy holds and the panel's never do reads back too.
	for _, p := range []model.Proxy{
		{Name: "W", Type: "vless", Server: "203.0.113.7", Port: 443, UUID: "u", UDP: true, TLS: &model.TLS{SNI: "a.example", Insecure: true},
			Transport: &model.Transport{Network: "ws", Path: "/w", Host: "cdn.example", Headers: map[string]string{"User-Agent": "x"}}},
		{Name: "U", Type: "tuic", Server: "203.0.113.7", Port: 443, UUID: "u", Password: "p", ALPN: []string{"h3"}, UDPRelay: "quic", UDP: true, TLS: &model.TLS{}},
		{Name: "S", Type: "shadowsocks", Server: "203.0.113.7", Port: 8388, Cipher: "aes-128-gcm", Password: "x", UDP: true,
			Plugin: "v2ray-plugin", PluginOpts: map[string]any{"mode": "websocket", "tls": true, "path": "/w"}},
		{Name: "T", Type: "trojan", Server: "203.0.113.7", Port: 443, Password: "p", TLS: &model.TLS{SNI: "a.example"}, Transport: &model.Transport{Network: "tcp"},
			Smux: &model.Smux{Protocol: "yamux", MaxStreams: 8, Padding: true, OnlyTCP: true, Brutal: &model.Brutal{Up: "50", Down: "100"}}},
	} {
		if got, ok := ReadProxy(Proxy(p)); !ok || !reflect.DeepEqual(got, p) {
			t.Errorf("%s:\n got %+v\nwant %+v", p.Name, got, p)
		}
	}
}

// A proxy of a template reads as the model holds it, or not at all where the model cannot
// hold it.
func TestReadProxy(t *testing.T) {
	ss := func(extra map[string]any) map[string]any {
		m := map[string]any{"name": "S", "type": "ss", "server": "203.0.113.7", "port": 8388, "cipher": "aes-128-gcm", "password": "x"}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	vless := func(extra map[string]any) map[string]any {
		m := map[string]any{"name": "V", "type": "vless", "server": "203.0.113.7", "port": 443, "uuid": "u"}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	tcp := &model.Transport{Network: "tcp"}
	for _, c := range []struct {
		name string
		in   map[string]any
		want *model.Proxy // nil: not read
	}{
		{"udp left out", ss(nil), &model.Proxy{Name: "S", Type: "shadowsocks", Server: "203.0.113.7", Port: 8388, Cipher: "aes-128-gcm", Password: "x", UDP: true}},
		{"udp off", ss(map[string]any{"udp": false}), &model.Proxy{Name: "S", Type: "shadowsocks", Server: "203.0.113.7", Port: 8388, Cipher: "aes-128-gcm", Password: "x"}},
		{"port as a string", ss(map[string]any{"port": "8388", "udp-over-tcp": true}),
			&model.Proxy{Name: "S", Type: "shadowsocks", Server: "203.0.113.7", Port: 8388, Cipher: "aes-128-gcm", Password: "x", UDP: true, UDPOverTCP: 1}},
		{"no port", ss(map[string]any{"port": nil}), nil},
		{"UDP over TCP 0", ss(map[string]any{"udp-over-tcp": true, "udp-over-tcp-version": 0}),
			&model.Proxy{Name: "S", Type: "shadowsocks", Server: "203.0.113.7", Port: 8388, Cipher: "aes-128-gcm", Password: "x", UDP: true, UDPOverTCP: 1}},
		{"UDP over TCP 2", ss(map[string]any{"udp-over-tcp": true, "udp-over-tcp-version": 2}),
			&model.Proxy{Name: "S", Type: "shadowsocks", Server: "203.0.113.7", Port: 8388, Cipher: "aes-128-gcm", Password: "x", UDP: true, UDPOverTCP: 2}},
		{"UDP over TCP 3", ss(map[string]any{"udp-over-tcp": true, "udp-over-tcp-version": 3}), nil},
		{"UDP over TCP turned off", ss(map[string]any{"udp-over-tcp": false, "udp-over-tcp-version": 3}),
			&model.Proxy{Name: "S", Type: "shadowsocks", Server: "203.0.113.7", Port: 8388, Cipher: "aes-128-gcm", Password: "x", UDP: true}},
		{"plugin", ss(map[string]any{"plugin": "obfs", "plugin-opts": map[string]any{"mode": "tls"}}),
			&model.Proxy{Name: "S", Type: "shadowsocks", Server: "203.0.113.7", Port: 8388, Cipher: "aes-128-gcm", Password: "x", UDP: true,
				Plugin: "obfs", PluginOpts: map[string]any{"mode": "tls"}}},
		{"smux", ss(map[string]any{"smux": map[string]any{"enabled": true}}),
			&model.Proxy{Name: "S", Type: "shadowsocks", Server: "203.0.113.7", Port: 8388, Cipher: "aes-128-gcm", Password: "x", UDP: true, Smux: &model.Smux{}}},
		{"smux of every setting", vless(map[string]any{"smux": map[string]any{"enabled": true, "protocol": "smux", "max-connections": 4, "min-streams": "2",
			"max-streams": 0, "padding": true, "only-tcp": true, "statistic": true, "brutal-opts": map[string]any{"enabled": true, "up": 50, "down": "100 Mbps"}}}),
			&model.Proxy{Name: "V", Type: "vless", Server: "203.0.113.7", Port: 443, UUID: "u", UDP: true, Transport: tcp,
				Smux: &model.Smux{Protocol: "smux", MaxConnections: 4, MinStreams: 2, Padding: true, OnlyTCP: true, Brutal: &model.Brutal{Up: "50", Down: "100 Mbps"}}}},
		{"smux of Brutal turned off", vless(map[string]any{"smux": map[string]any{"enabled": true, "brutal-opts": map[string]any{"enabled": false, "up": 50}}}),
			&model.Proxy{Name: "V", Type: "vless", Server: "203.0.113.7", Port: 443, UUID: "u", UDP: true, Transport: tcp, Smux: &model.Smux{}}},
		{"smux turned off", ss(map[string]any{"smux": map[string]any{"enabled": false}}),
			&model.Proxy{Name: "S", Type: "shadowsocks", Server: "203.0.113.7", Port: 8388, Cipher: "aes-128-gcm", Password: "x", UDP: true}},
		{"no TLS", vless(map[string]any{"servername": "a.example", "client-fingerprint": "chrome"}),
			&model.Proxy{Name: "V", Type: "vless", Server: "203.0.113.7", Port: 443, UUID: "u", UDP: true, Transport: tcp}},
		{"TLS", vless(map[string]any{"tls": true, "sni": "a.example", "skip-cert-verify": true, "fingerprint": "ab", "encryption": "none", "alpn": []any{"h2"}}),
			&model.Proxy{Name: "V", Type: "vless", Server: "203.0.113.7", Port: 443, UUID: "u", UDP: true, Transport: tcp, ALPN: []string{"h2"},
				TLS: &model.TLS{SNI: "a.example", Pin: "ab", Insecure: true}}},
		{"REALITY without tls", vless(map[string]any{"servername": "www.google.com", "reality-opts": map[string]any{"public-key": "k", "short-id": "ab"}}),
			&model.Proxy{Name: "V", Type: "vless", Server: "203.0.113.7", Port: 443, UUID: "u", UDP: true, Transport: tcp,
				TLS: &model.TLS{SNI: "www.google.com", Reality: &model.Reality{PublicKey: "k", ShortID: "ab"}}}},
		{"WebSocket", vless(map[string]any{"network": "ws", "ws-opts": map[string]any{"path": "/w", "headers": map[string]any{"host": "cdn.example"}}}),
			&model.Proxy{Name: "V", Type: "vless", Server: "203.0.113.7", Port: 443, UUID: "u", UDP: true,
				Transport: &model.Transport{Network: "ws", Path: "/w", Host: "cdn.example"}}},
		{"WebSocket headers but Host", vless(map[string]any{"network": "ws", "ws-opts": map[string]any{"headers": map[string]any{"Host": "cdn.example", "User-Agent": "x"}}}),
			&model.Proxy{Name: "V", Type: "vless", Server: "203.0.113.7", Port: 443, UUID: "u", UDP: true,
				Transport: &model.Transport{Network: "ws", Host: "cdn.example", Headers: map[string]string{"User-Agent": "x"}}}},
		{"HTTP/2", vless(map[string]any{"network": "h2"}), nil},
		{"Hysteria2 of a range alone", map[string]any{"name": "H", "type": "hysteria2", "server": "203.0.113.7", "ports": "20000-30000", "password": "p"},
			&model.Proxy{Name: "H", Type: "hysteria2", Server: "203.0.113.7", Ports: "20000-30000", Password: "p", UDP: true, TLS: &model.TLS{}}},
		{"Hysteria2 of neither", map[string]any{"name": "H", "type": "hysteria2", "server": "203.0.113.7", "password": "p"}, nil},
		{"TUIC of QUIC relaying", map[string]any{"name": "U", "type": "tuic", "server": "203.0.113.7", "port": 443, "uuid": "u", "password": "p",
			"udp-relay-mode": "quic", "udp": false},
			&model.Proxy{Name: "U", Type: "tuic", Server: "203.0.113.7", Port: 443, UUID: "u", Password: "p", UDPRelay: "quic", UDP: true, TLS: &model.TLS{}}},
		{"TUIC of native relaying", map[string]any{"name": "U", "type": "tuic", "server": "203.0.113.7", "port": 443, "uuid": "u", "password": "p",
			"udp-relay-mode": "native"},
			&model.Proxy{Name: "U", Type: "tuic", Server: "203.0.113.7", Port: 443, UUID: "u", Password: "p", UDP: true, TLS: &model.TLS{}}},
		{"VMess", map[string]any{"name": "M", "type": "vmess", "server": "203.0.113.7", "port": 443, "uuid": "u"}, nil},
	} {
		got, ok := ReadProxy(c.in)
		switch {
		case ok != (c.want != nil):
			t.Errorf("%s: read %v: %+v", c.name, ok, got)
		case ok && !reflect.DeepEqual(got, *c.want):
			t.Errorf("%s:\n got %+v\nwant %+v", c.name, got, *c.want)
		}
	}
}
