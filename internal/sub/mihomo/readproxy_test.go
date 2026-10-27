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
		{"plugin", ss(map[string]any{"plugin": "obfs", "plugin-opts": map[string]any{"mode": "tls"}}), nil},
		{"smux", ss(map[string]any{"smux": map[string]any{"enabled": true}}), nil},
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
		{"WebSocket header but Host", vless(map[string]any{"network": "ws", "ws-opts": map[string]any{"headers": map[string]any{"User-Agent": "x"}}}), nil},
		{"HTTP/2", vless(map[string]any{"network": "h2"}), nil},
		{"TUIC of QUIC relaying", map[string]any{"name": "U", "type": "tuic", "server": "203.0.113.7", "port": 443, "uuid": "u", "password": "p",
			"udp-relay-mode": "quic", "udp": false},
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
