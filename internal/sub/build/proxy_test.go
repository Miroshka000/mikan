package build_test

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"mikan/internal/proto"
	"mikan/internal/sub/build"
	"mikan/internal/sub/mihomo"
	"mikan/internal/sub/xray"
)

func realityKey(t *testing.T) (private, public string) {
	t.Helper()
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	b := base64.RawURLEncoding
	return b.EncodeToString(k.Bytes()), b.EncodeToString(k.PublicKey().Bytes())
}

func mustParse(t *testing.T, src string) proto.Template {
	t.Helper()
	tpl, err := proto.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	return tpl
}

var keys = proto.Slot{Name: "s000001", UUID: "0b4ddc4c-7c4f-4a36-9d62-6f1a44b8c4e1", Secret: "S3cr3t+/="}

// client is the user's proxy of tpl as mihomo writes it, and its share link.
func client(t *testing.T, tpl proto.Template, in build.Input) (map[string]any, string) {
	t.Helper()
	p, err := build.Proxy(tpl, in)
	if err != nil {
		t.Fatal(err)
	}
	return mihomo.Proxy(p), xray.Link(p)
}

func reality(priv string) string {
	return "reality-config:\n  dest: www.microsoft.com:443\n  private-key: " + priv + "\n  short-id: [a1b2]\n  server-names: [www.microsoft.com]\n"
}

// The default presets' templates give their apps what each protocol needs: REALITY's key
// and name, XHTTP's path and connection reuse, the pin of a self-signed certificate.
func TestPresetsRender(t *testing.T) {
	priv, pub := realityKey(t)
	cases := map[string]string{
		"vless_reality_vision": "type: vless\n" + reality(priv) + "mikan:\n  flow: xtls-rprx-vision\n",
		"vless_reality_xhttp":  "type: vless\n" + reality(priv) + "xhttp-config:\n  path: /x7k2\n  mode: stream-one\n",
		"hysteria2":            "type: hysteria2\nalpn: [h3]\nobfs: salamander\nobfs-password: obfs123\n",
		"tuic_v5":              "type: tuic\nalpn: [h3]\ncongestion-controller: bbr\n",
	}
	for preset, src := range cases {
		tpl := mustParse(t, proto.Marshal(mustParse(t, src)))
		m, link := client(t, tpl, build.Input{Name: "N", Host: "203.0.113.7", Port: 443, PortSpec: "443", PinSHA256: "ab12", Keys: keys})
		u, err := url.Parse(link)
		if err != nil {
			t.Fatalf("%s uri: %v", preset, err)
		}
		q := u.Query()
		switch preset {
		case "vless_reality_vision":
			if q.Get("pbk") != pub || q.Get("sid") != "a1b2" || q.Get("flow") != "xtls-rprx-vision" || q.Get("type") != "tcp" || q.Get("sni") != "www.microsoft.com" || m["udp"] != true {
				t.Fatalf("vision client: %s %v", link, m)
			}
		case "vless_reality_xhttp":
			if q.Get("type") != "xhttp" || q.Get("path") != "/x7k2" || q.Get("mode") != "stream-one" || q.Get("flow") != "" || m["udp"] != true {
				t.Fatalf("xhttp client: %s %v", link, m)
			}
			if opts := m["xhttp-opts"].(map[string]any); opts["reuse-settings"] == nil {
				t.Fatalf("xhttp must multiplex: %v", opts)
			}
		case "hysteria2":
			if u.Scheme != "hysteria2" || q.Get("obfs") != "salamander" || q.Get("pinSHA256") != "ab12" || m["fingerprint"] != "ab12" {
				t.Fatalf("hy2 client: %s %v", link, m)
			}
		case "tuic_v5":
			if pw, _ := u.User.Password(); u.Scheme != "tuic" || pw != "S3cr3t+/=" || q.Get("allow_insecure") != "1" || m["congestion-controller"] != "bbr" {
				t.Fatalf("tuic client: %s %v", link, m)
			}
		}
	}
}

// Behind a TCP proxy (GitHub issue #11) clients get the proxy's address, port and TLS
// name; an IPv6 proxy address is bracketed in links.
func TestClientEndpoint(t *testing.T) {
	tpl := mustParse(t, "type: trojan\nws-path: /ws\nmikan:\n  tls: node\n  client:\n    fingerprint: ios\n")
	if err := proto.SetClientEndpoint(tpl, "vpn.example.com", 443, "trojan.example.com", ""); err != nil {
		t.Fatal(err)
	}
	m, link := client(t, tpl, build.Input{Name: "X", Host: "203.0.113.7", Port: 8444, SNI: "node.example.com", Keys: keys})
	u, _ := url.Parse(link)
	if u.Host != "vpn.example.com:443" || u.Query().Get("sni") != "trojan.example.com" || u.Query().Get("fp") != "ios" {
		t.Fatalf("link: %s", link)
	}
	if m["server"] != "vpn.example.com" || m["port"] != 443 || m["sni"] != "trojan.example.com" {
		t.Fatalf("profile: %v", m)
	}
	if err := proto.SetClientEndpoint(tpl, "2001:db8::1", 443, "", ""); err != nil {
		t.Fatal(err)
	}
	_, link = client(t, tpl, build.Input{Name: "X", Host: "203.0.113.7", Port: 8444, Keys: keys})
	if u, _ := url.Parse(link); u.Host != "[2001:db8::1]:443" {
		t.Fatalf("ipv6: %s", link)
	}
}

// Input.Client replaces mikan.client field by field, and an empty field keeps the
// template's: a caller that passes none (the relay of a node cascade) gets it whole.
func TestInputClient(t *testing.T) {
	in := build.Input{Name: "X", Host: "203.0.113.7", Port: 8444, SNI: "node.example.com", PinSHA256: "ab12", Keys: keys}
	tpl := mustParse(t, "type: vless\nws-path: /ws\nmikan:\n  tls: node\n  client:\n    fingerprint: ios\n")
	if err := proto.SetClientEndpoint(tpl, "vpn.example.com", 443, "tpl.example.com", "tpl-host.example.com"); err != nil {
		t.Fatal(err)
	}
	host := func(m map[string]any) string {
		ws, _ := m["ws-opts"].(map[string]any)
		h, _ := ws["headers"].(map[string]any)
		s, _ := h["Host"].(string)
		return s
	}
	m, _ := client(t, tpl, in)
	if m["server"] != "vpn.example.com" || m["port"] != 443 || m["servername"] != "tpl.example.com" || host(m) != "tpl-host.example.com" || m["client-fingerprint"] != "ios" {
		t.Fatalf("no Input.Client: %v", m)
	}
	in.Client = proto.ClientOverrides{Server: "edge.example.com", SNI: "sni.example.com", Host: "host.example.com"}
	m, link := client(t, tpl, in)
	if m["server"] != "edge.example.com" || m["port"] != 443 || m["servername"] != "sni.example.com" || host(m) != "host.example.com" || m["client-fingerprint"] != "ios" {
		t.Fatalf("Input.Client: %v", m)
	}
	if !strings.Contains(link, "@edge.example.com:443") || !strings.Contains(link, "sni=sni.example.com") || !strings.Contains(link, "host=host.example.com") || !strings.Contains(link, "fp=ios") {
		t.Fatalf("Input.Client link: %s", link)
	}
	// Not behind a CDN the pin stays, whoever moved the address.
	if m["fingerprint"] != "ab12" {
		t.Fatalf("pin dropped: %v", m)
	}

	// The SNI of REALITY and ShadowQUIC, and a port that replaces hopping, come from it too.
	priv, _ := realityKey(t)
	c := proto.ClientOverrides{SNI: "sni.example.com"}
	r, _ := client(t, mustParse(t, "type: vless\n"+reality(priv)), build.Input{Name: "X", Host: "203.0.113.7", Port: 443, Keys: keys, Client: c})
	q, _ := client(t, mustParse(t, "type: shadowquic\njls-upstream: {addr: www.google.com:443, sni: www.google.com}\n"), build.Input{Name: "X", Host: "203.0.113.7", Port: 443, Keys: keys, Client: c})
	if r["servername"] != "sni.example.com" || q["sni"] != "sni.example.com" {
		t.Fatalf("sni: reality %v, shadowquic %v", r, q)
	}
	hy2 := build.Input{Name: "X", Host: "203.0.113.7", Port: 20000, PortSpec: "20000-20100", Keys: keys, Client: proto.ClientOverrides{Port: 443}}
	if m, _ := client(t, mustParse(t, "type: hysteria2\n"), hy2); m["ports"] != nil || m["port"] != 443 {
		t.Fatalf("hopping past an Input.Client port: %v", m)
	}
}

// A port of the client side's own is the one clients dial: the node's hopping range would
// point past the proxy.
func TestClientPortDropsHopping(t *testing.T) {
	in := build.Input{Name: "X", Host: "203.0.113.7", Port: 20000, PortSpec: "20000-20100", SNI: "vpn.example.com", Keys: keys}
	hy2 := mustParse(t, "type: hysteria2\n")
	m, link := client(t, hy2, in)
	if m["ports"] != "20000-20100" || !strings.Contains(link, "mport=20000-20100") {
		t.Fatalf("hopping without an override: %s %v", link, m)
	}
	if err := proto.SetClientEndpoint(hy2, "", 443, "", ""); err != nil {
		t.Fatal(err)
	}
	m, link = client(t, hy2, in)
	u, _ := url.Parse(link)
	if m["ports"] != nil || u.Query().Has("mport") || m["port"] != 443 {
		t.Fatalf("override: %s %v", link, m)
	}
}

// Through a CDN the apps get the CDN's address, name and Host, and no pin of the node's
// self-signed certificate: they see the CDN's.
func TestCDNClients(t *testing.T) {
	in := build.Input{Name: "X", Host: "203.0.113.7", Port: 2053, PinSHA256: "ab12", SNI: "node.example.com", Keys: keys}
	for _, src := range []string{
		"type: vless\nws-path: /w\nmikan: {tls: node}\n",
		"type: vless\nxhttp-config: {path: /x, mode: packet-up}\nmikan: {tls: node}\n",
	} {
		tpl := mustParse(t, src)
		if err := proto.SetClientEndpoint(tpl, "cdn.example.com", 443, "cdn.example.com", "edge.example.com"); err != nil {
			t.Fatal(err)
		}
		if err := proto.SetCDN(tpl, &proto.CDN{}); err != nil {
			t.Fatal(err)
		}
		m, link := client(t, tpl, in)
		if m["server"] != "cdn.example.com" || m["port"] != 443 || m["servername"] != "cdn.example.com" {
			t.Errorf("%s: endpoint %v", src, m)
		}
		if _, pinned := m["fingerprint"]; pinned || strings.Contains(link, "pcs=") || strings.Contains(link, "insecure") {
			t.Errorf("%s: pinned behind a CDN: %s %v", src, link, m)
		}
		if !strings.HasPrefix(link, "vless://") || !strings.Contains(link, "host=edge.example.com") {
			t.Errorf("%s: link %s", src, link)
		}
		host := ""
		if ws, ok := m["ws-opts"].(map[string]any); ok {
			h, _ := ws["headers"].(map[string]any)
			host, _ = h["Host"].(string)
		}
		if x, ok := m["xhttp-opts"].(map[string]any); ok {
			host, _ = x["host"].(string)
		}
		if host != "edge.example.com" {
			t.Errorf("%s: Host %q", src, host)
		}
	}
	// Not behind a CDN, the pin stays.
	if m, _ := client(t, mustParse(t, "type: vless\nws-path: /w\nmikan: {tls: node}\n"), in); m["fingerprint"] != "ab12" {
		t.Fatalf("the pin without a CDN: %v", m)
	}
}

// The protocols of mihomo's newer cores reach mihomo profiles only; Shadowsocks has an
// ss:// link, with the user's own key next to the server's.
func TestExtraTypes(t *testing.T) {
	key := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	ssKey := base64.StdEncoding.EncodeToString(key)
	key32 := make([]byte, 32)
	if _, err := rand.Read(key32); err != nil {
		t.Fatal(err)
	}
	ssKey32 := base64.StdEncoding.EncodeToString(key32)
	in := build.Input{Name: "X", Host: "vpn.example.com", Port: 4443, SNI: "vpn.example.com", Keys: keys}
	for src, check := range map[string]func(m map[string]any, link string){
		"type: trusttunnel\ncongestion-controller: bbr\n": func(m map[string]any, link string) {
			if m["type"] != "trusttunnel" || m["username"] != "s000001" || m["password"] != "S3cr3t+/=" || m["sni"] != "vpn.example.com" || m["client-fingerprint"] != "chrome" || m["udp"] != true || m["tls"] != nil {
				t.Errorf("trusttunnel client: %v", m)
			}
			if link != "" {
				t.Errorf("no share link format: %s", link)
			}
		},
		"type: shadowquic\njls-upstream: {addr: www.google.com:443, sni: www.google.com}\nalpn: [h3]\n": func(m map[string]any, link string) {
			if m["type"] != "shadowquic" || m["username"] != "s000001" || m["password"] != "S3cr3t+/=" || m["sni"] != "www.google.com" || m["udp"] != true || link != "" {
				t.Errorf("shadowquic client: %v %s", m, link)
			}
			if a, _ := m["alpn"].([]string); len(a) != 1 || a[0] != "h3" {
				t.Errorf("shadowquic alpn: %v", m["alpn"])
			}
		},
		"type: mieru\n": func(m map[string]any, link string) {
			// The Citrus server knows who sends UDP over Mieru: UDP on.
			if m["type"] != "mieru" || m["transport"] != "TCP" || m["username"] != "s000001" || m["password"] != "S3cr3t+/=" || m["udp"] != true || link != "" {
				t.Errorf("mieru client: %v %s", m, link)
			}
		},
		"type: shadowsocks\ncipher: 2022-blake3-aes-128-gcm\npassword: " + ssKey + "\n": func(m map[string]any, link string) {
			// A key per user (EIH): the app gets the server's key and its own.
			own := proto.SSUserKey("2022-blake3-aes-128-gcm", "S3cr3t+/=")
			if m["type"] != "ss" || m["cipher"] != "2022-blake3-aes-128-gcm" || m["password"] != ssKey+":"+own || m["udp"] != true ||
				m["udp-over-tcp"] != true || m["udp-over-tcp-version"] != 2 {
				t.Errorf("shadowsocks client: %v", m)
			}
			u, err := url.Parse(link)
			if err != nil || u.Scheme != "ss" || u.User.Username() != "2022-blake3-aes-128-gcm" || u.Host != "vpn.example.com:4443" {
				t.Fatalf("ss link: %s %v", link, err)
			}
			if p, _ := u.User.Password(); p != ssKey+":"+own {
				t.Errorf("ss link key: %q", p)
			}
		},
		"type: shadowsocks\ncipher: 2022-blake3-chacha20-poly1305\npassword: " + ssKey32 + "\n": func(m map[string]any, link string) {
			// ChaCha20 has no identity header: one key for everyone.
			if m["password"] != ssKey32 || m["udp"] != true || m["udp-over-tcp"] != true {
				t.Errorf("shadowsocks client: %v", m)
			}
		},
		"type: sudoku\nkey: 6f1a44b8-c4e1-4a36-9d62-0b4ddc4c7c4f\naead-method: chacha20-poly1305\npadding-min: 2\npadding-max: 7\ntable-type: prefer_ascii\nenable-pure-downlink: false\n": func(m map[string]any, link string) {
			if m["type"] != "sudoku" || m["key"] != "6f1a44b8-c4e1-4a36-9d62-0b4ddc4c7c4f" || m["aead-method"] != "chacha20-poly1305" || m["table-type"] != "prefer_ascii" || m["padding-min"] != 2 || m["padding-max"] != 7 ||
				m["enable-pure-downlink"] != false || m["udp"] != true || link != "" {
				t.Errorf("sudoku client: %v %s", m, link)
			}
		},
		"type: snell\npsk: p5k-Secret\nversion: 3\n": func(m map[string]any, link string) {
			if m["type"] != "snell" || m["psk"] != "p5k-Secret" || m["version"] != 3 || m["udp"] != true || link != "" {
				t.Errorf("snell client: %v %s", m, link)
			}
		},
	} {
		m, link := client(t, mustParse(t, src), in)
		check(m, link)
	}
	// A node older than the key per user takes the server's key alone.
	in.SSOneKey = true
	if m, _ := client(t, mustParse(t, "type: shadowsocks\ncipher: 2022-blake3-aes-128-gcm\npassword: "+ssKey+"\n"), in); m["password"] != ssKey {
		t.Fatalf("one key: %v", m["password"])
	}
}

// The inbound's own fingerprint wins over the panel's default, which wins over chrome; an
// own value must have the shape of a profile name, or it never reaches apps.
func TestFingerprintPrecedence(t *testing.T) {
	priv, _ := realityKey(t)
	for _, c := range []struct{ own, panel, want string }{
		{"", "", "chrome"},
		{"", "firefox", "firefox"},
		{"safari", "firefox", "safari"},
		{"chrome120", "ios", "chrome120"}, // an own value of the right shape is used
		{"Chrome 1,2", "ios", "ios"},      // a malformed one never reaches apps: the default
		{"", "bad value!", "chrome"},      // a malformed default: chrome
		{"randomized", "", "randomized"},
	} {
		src := "type: vless\n" + reality(priv)
		if c.own != "" {
			src += "mikan:\n  client:\n    fingerprint: " + c.own + "\n"
		}
		m, link := client(t, mustParse(t, src), build.Input{Name: "X", Host: "203.0.113.7", Port: 443, Fingerprint: c.panel, Keys: keys})
		u, _ := url.Parse(link)
		if u.Query().Get("fp") != c.want || m["client-fingerprint"] != c.want {
			t.Errorf("own %q panel %q: fp=%s client-fingerprint=%v, want %s", c.own, c.panel, u.Query().Get("fp"), m["client-fingerprint"], c.want)
		}
	}
}

// Node-certificate TLS gets the fingerprint too; QUIC protocols have no uTLS and no fp.
func TestFingerprintByTransport(t *testing.T) {
	in := build.Input{Name: "X", Host: "vpn.example.com", Port: 2053, SNI: "vpn.example.com", Fingerprint: "edge", Keys: keys}
	m, link := client(t, mustParse(t, "type: trojan\nws-path: /ws\nmikan:\n  tls: node\n"), in)
	u, _ := url.Parse(link)
	if u.Query().Get("fp") != "edge" || m["client-fingerprint"] != "edge" {
		t.Fatalf("trojan tls: %s %v", link, m)
	}
	m, link = client(t, mustParse(t, "type: hysteria2\n"), in)
	u, _ = url.Parse(link)
	if u.Query().Has("fp") || m["client-fingerprint"] != nil {
		t.Fatalf("hysteria2 has no uTLS: %s %v", link, m)
	}
	// AnyTLS links carry no fp (the scheme has none), the mihomo profile does.
	m, link = client(t, mustParse(t, "type: anytls\n"), in)
	if m["client-fingerprint"] != "edge" || strings.Contains(link, "fp=") || strings.Contains(link, "security=") {
		t.Fatalf("anytls: %s %v", link, m)
	}
}

// The client gets the server's obfuscation and Gecko's sizes.
func TestGeckoClient(t *testing.T) {
	tpl := mustParse(t, "type: hysteria2\nobfs: gecko\nobfs-password: pw\nobfs-min-packet-size: 600\nobfs-max-packet-size: 1100\n")
	y, link := client(t, tpl, build.Input{Name: "G", Host: "203.0.113.7", Port: 2443, Keys: keys})
	if y["obfs"] != "gecko" || y["obfs-password"] != "pw" || y["obfs-min-packet-size"] != 600 || y["obfs-max-packet-size"] != 1100 {
		t.Fatalf("mihomo: %v", y)
	}
	if !strings.Contains(link, "obfs=gecko") {
		t.Fatalf("link: %s", link)
	}
}

// VLESS Encryption: clients get the public halves with a full handshake (1rtt), in both
// formats, and never the server's keys.
func TestVLESSEncryption(t *testing.T) {
	priv, _ := realityKey(t)
	x := make([]byte, 32)
	_, _ = rand.Read(x)
	xk, _ := ecdh.X25519().NewPrivateKey(x)
	b64 := base64.RawURLEncoding.EncodeToString
	tpl := mustParse(t, "type: vless\nxhttp-config: {path: /p, mode: stream-one}\ndecryption: mlkem768x25519plus.native.600s."+b64(x)+"\n"+reality(priv))
	m, link := client(t, tpl, build.Input{Name: "PQ", Host: "203.0.113.7", Port: 2096, Keys: keys})
	enc := "mlkem768x25519plus.native.1rtt." + b64(xk.PublicKey().Bytes())
	u, _ := url.Parse(link)
	if m["encryption"] != enc || u.Query().Get("encryption") != enc || strings.Contains(link, b64(x)) {
		t.Fatalf("client: %v\n%s", m["encryption"], link)
	}
	// Without it the link says none, and the profile nothing.
	m, link = client(t, mustParse(t, "type: vless\n"+reality(priv)), build.Input{Name: "X", Host: "203.0.113.7", Port: 443, Keys: keys})
	if u, _ := url.Parse(link); m["encryption"] != nil || u.Query().Get("encryption") != "none" {
		t.Fatalf("plain: %v %s", m["encryption"], link)
	}
}

// A self-signed node certificate is pinned in links too: Xray apps read pcs, Hysteria2
// apps pinSHA256. TUIC links have no pin field.
func TestSelfSignedLinksCarryThePin(t *testing.T) {
	in := build.Input{Name: "X", Host: "203.0.113.7", Port: 2083, PinSHA256: "ab12", Keys: keys}
	for src, want := range map[string]string{
		"type: trojan\nmikan: {tls: node}\n":             "pcs=ab12",
		"type: anytls\n":                                 "pcs=ab12",
		"type: vless\nws-path: /v\nmikan: {tls: node}\n": "pcs=ab12",
		"type: hysteria2\n":                              "pinSHA256=ab12",
		"type: tuic\n":                                   "allow_insecure=1",
	} {
		m, link := client(t, mustParse(t, src), in)
		if !strings.Contains(link, want) || m["fingerprint"] != "ab12" || m["skip-cert-verify"] != false {
			t.Errorf("%s: %s lacks %s (mihomo %v)", src, link, want, m["fingerprint"])
		}
	}
}

func TestXHTTPSettingsReachTheClient(t *testing.T) {
	priv, _ := realityKey(t)
	src := "type: vless\nxhttp-config: {path: /x, mode: stream-one, x-padding-bytes: 2000-3000, x-padding-obfs-mode: true, sc-max-buffered-posts: 30}\n" + reality(priv)
	m, link := client(t, mustParse(t, src), build.Input{Name: "X", Host: "203.0.113.7", Port: 443, Keys: keys})
	opts := m["xhttp-opts"].(map[string]any)
	if opts["x-padding-bytes"] != "2000-3000" || opts["x-padding-obfs-mode"] != true || opts["sc-max-buffered-posts"] != nil {
		t.Fatalf("xhttp-opts: %v", opts)
	}
	u, _ := url.Parse(link)
	if extra := u.Query().Get("extra"); extra != `{"xPaddingBytes":"2000-3000","xPaddingObfsMode":true}` {
		t.Fatalf("link extra: %q", extra)
	}
	_, plain := client(t, mustParse(t, strings.Replace(src, ", x-padding-bytes: 2000-3000, x-padding-obfs-mode: true", "", 1)), build.Input{Name: "X", Host: "203.0.113.7", Port: 443, Keys: keys})
	if strings.Contains(plain, "extra=") {
		t.Fatalf("no shared settings, no extra: %s", plain)
	}
	// A number typed in YAML reaches both as a number.
	m, link = client(t, mustParse(t, strings.Replace(src, "x-padding-bytes: 2000-3000", "uplink-chunk-size: 100000", 1)), build.Input{Name: "X", Host: "203.0.113.7", Port: 443, Keys: keys})
	if u, _ := url.Parse(link); m["xhttp-opts"].(map[string]any)["uplink-chunk-size"] != 100000 || !strings.Contains(u.Query().Get("extra"), `"uplinkChunkSize":100000`) {
		t.Fatalf("a number: %v %s", m["xhttp-opts"], link)
	}
}

// The listeners' other transports and protocols, as their apps get them.
func TestNewTypes(t *testing.T) {
	priv, _ := realityKey(t)
	in := build.Input{Name: "X", Host: "vpn.example.com", Port: 2053, SNI: "vpn.example.com", Keys: keys}
	for src, check := range map[string]func(map[string]any, string){
		"type: vless\ngrpc-service-name: api\nreality-config: {dest: www.microsoft.com:443, private-key: " + priv + ", short-id: [ab], server-names: [www.microsoft.com]}\n": func(m map[string]any, link string) {
			if !strings.Contains(link, "type=grpc") || !strings.Contains(link, "serviceName=api") || !strings.Contains(link, "mode=gun") || m["network"] != "grpc" {
				t.Errorf("grpc: %s %v", link, m)
			}
		},
		"type: trojan\nws-path: /t\nmikan: {tls: node}\n": func(m map[string]any, link string) {
			if !strings.HasPrefix(link, "trojan://") || m["sni"] != "vpn.example.com" || m["password"] != "S3cr3t+/=" || m["tls"] != true {
				t.Errorf("trojan: %s %v", link, m)
			}
		},
		"type: anytls\n": func(m map[string]any, link string) {
			if !strings.HasPrefix(link, "anytls://") || m["sni"] != "vpn.example.com" || m["udp"] != true {
				t.Errorf("anytls: %s %v", link, m)
			}
		},
		"type: vless\nws-path: /v\nmikan: {tls: node, client: {server: cdn.example.com, port: 443}}\n": func(m map[string]any, link string) {
			if m["server"] != "cdn.example.com" || m["port"] != 443 || !strings.HasPrefix(link, "vless://") || !strings.Contains(link, "@cdn.example.com:443?") {
				t.Errorf("vless override: %s %v", link, m)
			}
		},
	} {
		m, link := client(t, mustParse(t, src), in)
		check(m, link)
	}
}

// A REALITY server that requires X25519MLKEM768 gets clients that offer it; the others
// leave it to the app.
func TestRealityRequiredPostQuantum(t *testing.T) {
	priv, _ := realityKey(t)
	src := "type: vless\n" + reality(priv)
	in := build.Input{Name: "X", Host: "vpn.example.com", Port: 443, Keys: keys}
	for extra, want := range map[string]any{"": nil, "  require-x25519mlkem768: true\n": true} {
		m, _ := client(t, mustParse(t, src+extra), in)
		if got := m["reality-opts"].(map[string]any)["support-x25519mlkem768"]; got != want {
			t.Fatalf("%q: support-x25519mlkem768 %v, want %v", extra, got, want)
		}
	}
}

func xhttpTemplate(t *testing.T, xhttp string) proto.Template {
	t.Helper()
	priv, _ := realityKey(t)
	return mustParse(t, "type: vless\nxhttp-config: {path: /x"+xhttp+"}\n"+reality(priv))
}

func extraOf(t *testing.T, link string) map[string]any {
	t.Helper()
	u, _ := url.Parse(link)
	var extra map[string]any
	if err := json.Unmarshal([]byte(u.Query().Get("extra")), &extra); err != nil {
		t.Fatalf("extra of %s: %v", link, err)
	}
	return extra
}

// The session, order and data keys reach every format that carries XHTTP: the server's
// value in mihomo's profile and in the Xray link's extra under Xray's own names; the id
// table is the client's own. The server-only keys stay at the server.
func TestXHTTPSessionSettingsReachTheClient(t *testing.T) {
	in := build.Input{Name: "X", Host: "203.0.113.7", Port: 443, Keys: keys}
	tpl := xhttpTemplate(t, ", mode: packet-up")
	err := proto.SetXHTTP(tpl, proto.XHTTPTuning{Mode: "packet-up", SessionPlacement: "header", SessionKey: "X-Sid", SeqPlacement: "query", SeqKey: "n",
		UplinkDataPlacement: "cookie", UplinkDataKey: "d", StreamUpServerSecs: "5-9", MaxBufferedPosts: "50", NoSSEHeader: true,
		SessionTable: "hex", SessionLength: "20-24"})
	if err != nil {
		t.Fatal(err)
	}
	m, link := client(t, tpl, in)
	opts := m["xhttp-opts"].(map[string]any)
	for k, want := range map[string]string{"session-placement": "header", "session-key": "X-Sid", "seq-placement": "query", "seq-key": "n",
		"uplink-data-placement": "cookie", "uplink-data-key": "d", "session-table": "hex", "session-length": "20-24"} {
		if opts[k] != want {
			t.Errorf("mihomo %s = %v, want %s (%v)", k, opts[k], want, opts)
		}
	}
	for _, k := range []string{"sc-stream-up-server-secs", "sc-max-buffered-posts", "no-sse-header"} {
		if _, has := opts[k]; has {
			t.Errorf("mihomo got the server's %s", k)
		}
	}
	extra := extraOf(t, link)
	for k, want := range map[string]string{"sessionIDPlacement": "header", "sessionIDKey": "X-Sid", "seqPlacement": "query", "seqKey": "n",
		"uplinkDataPlacement": "cookie", "uplinkDataKey": "d", "sessionIDTable": "hex", "sessionIDLength": "20-24"} {
		if extra[k] != want {
			t.Errorf("link %s = %v, want %s (%v)", k, extra[k], want, extra)
		}
	}
	for _, k := range []string{"scStreamUpServerSecs", "scMaxBufferedPosts", "noSSEHeader", "sessionPlacement", "sessionKey"} {
		if _, has := extra[k]; has {
			t.Errorf("link has %s", k)
		}
	}

	// A table without a length: Xray would find it too small, so the link says mihomo's default.
	if err := proto.SetXHTTP(tpl, proto.XHTTPTuning{SessionTable: "Alphabet"}); err != nil {
		t.Fatal(err)
	}
	m, link = client(t, tpl, in)
	extra = extraOf(t, link)
	if o := m["xhttp-opts"].(map[string]any); o["session-length"] != "16-32" || extra["sessionIDLength"] != "16-32" || extra["sessionIDTable"] != "Alphabet" {
		t.Fatalf("default length: %v %v", o, extra)
	}

	// Nothing set: nothing sent.
	if err := proto.SetXHTTP(tpl, proto.XHTTPTuning{}); err != nil {
		t.Fatal(err)
	}
	m, link = client(t, tpl, in)
	for _, k := range []string{"session-table", "session-length", "session-placement", "uplink-data-placement"} {
		if _, has := m["xhttp-opts"].(map[string]any)[k]; has {
			t.Errorf("defaults still send %s", k)
		}
	}
	if strings.Contains(link, "sessionID") {
		t.Errorf("defaults in the link: %s", link)
	}
}

// The apps' own upload over the server's reaches mihomo's profile and the Xray link's extra.
func TestXHTTPClientUplink(t *testing.T) {
	tpl := xhttpTemplate(t, ", mode: packet-up")
	x := proto.XHTTPTuning{Mode: "packet-up", UplinkMethod: "POST", UplinkDataPlacement: "body", ClientUplinkMethod: "GET", ClientUplinkPlacement: "body", ClientUplinkChunkSize: "512000"}
	if err := proto.SetXHTTP(tpl, x); err != nil {
		t.Fatal(err)
	}
	m, link := client(t, tpl, build.Input{Name: "X", Host: "203.0.113.7", Port: 443, Keys: keys})
	opts := m["xhttp-opts"].(map[string]any)
	if opts["uplink-http-method"] != "GET" || opts["uplink-data-placement"] != "body" || opts["uplink-chunk-size"] != "512000" {
		t.Fatalf("mihomo: %v", opts)
	}
	if extra := extraOf(t, link); extra["uplinkHTTPMethod"] != "GET" || extra["uplinkDataPlacement"] != "body" || extra["uplinkChunkSize"] != "512000" {
		t.Fatalf("link: %v", extra)
	}
}

// The client's own reuse settings replace the panel's, in mihomo's profile and in the
// Xray link's extra.
func TestXHTTPClientReuse(t *testing.T) {
	in := build.Input{Name: "X", Host: "203.0.113.7", Port: 443, Keys: keys}
	tpl := xhttpTemplate(t, "")
	plain, _ := client(t, tpl, in)
	if r := plain["xhttp-opts"].(map[string]any)["reuse-settings"].(map[string]any); r["max-concurrency"] != "16-32" {
		t.Fatalf("the panel's default: %v", r)
	}
	if err := proto.SetXHTTP(tpl, proto.XHTTPTuning{MaxConnections: "2-4", MaxReusableSecs: "600", MinPostsIntervalMs: "20"}); err != nil {
		t.Fatal(err)
	}
	m, link := client(t, tpl, in)
	opts := m["xhttp-opts"].(map[string]any)
	r := opts["reuse-settings"].(map[string]any)
	if r["max-connections"] != "2-4" || r["h-max-reusable-secs"] != "600" || r["max-concurrency"] != nil || opts["sc-min-posts-interval-ms"] != "20" {
		t.Fatalf("mihomo: %v", opts)
	}
	extra := extraOf(t, link)
	xm, _ := extra["xmux"].(map[string]any)
	if xm["maxConnections"] != "2-4" || xm["hMaxReusableSecs"] != "600" || extra["scMinPostsIntervalMs"] != "20" || strings.Contains(link, "maxConcurrency") {
		t.Fatalf("link extra: %v", extra)
	}
}

// A template that does not validate makes no proxy: its apps would not connect.
func TestBrokenTemplateMakesNoProxy(t *testing.T) {
	for _, src := range []string{"type: vless\nws-path: /ws\n", "type: socks\n"} {
		if _, err := build.Proxy(mustParse(t, src), build.Input{Name: "X", Host: "203.0.113.7", Port: 443, Keys: keys}); err == nil {
			t.Errorf("%q made a proxy", src)
		}
	}
}

// Sudoku's httpmask and Snell's obfs-opts reach the client as the template holds them,
// numbers as numbers.
func TestSectionsReachTheClient(t *testing.T) {
	in := build.Input{Name: "X", Host: "vpn.example.com", Port: 4443, Keys: keys}
	for src, want := range map[string]map[string]any{
		"type: sudoku\nkey: k\nhttpmask: {disable: false, mode: ws, path-root: 2024}\n": {"httpmask": map[string]any{"disable": false, "mode": "ws", "path-root": 2024}},
		"type: snell\npsk: p5k-Secret\nversion: 4\nobfs-opts: {mode: tls, host: 443}\n": {"obfs-opts": map[string]any{"mode": "tls", "host": 443}},
	} {
		m, _ := client(t, mustParse(t, src), in)
		for k, v := range want {
			if !reflect.DeepEqual(m[k], v) {
				t.Errorf("%s: %#v, want %#v, numbers as int", k, m[k], v)
			}
		}
	}
}
