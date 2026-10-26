package proto

import (
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
)

// The protocols of mihomo's newer cores: TrustTunnel, ShadowQUIC and Mieru check every
// user, and so does Shadowsocks-2022 with an AES method; Sudoku, Snell and
// Shadowsocks-2022 with ChaCha20 have one key for everyone. What their apps get is
// internal/sub/build's.
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
	cert := Cert{CertPath: "/c", KeyPath: "/k"}
	cases := []struct {
		src    string
		shared bool
		check  func(l map[string]any)
	}{
		{"type: trusttunnel\ncongestion-controller: bbr\n", false, func(l map[string]any) {
			us, _ := l["users"].([]map[string]any)
			if len(us) != 2 || us[0]["username"] != "s000001" || us[0]["password"] != "S3cr3t+/=" || l["certificate"] != "/c" {
				t.Errorf("trusttunnel listener: %v", l)
			}
			if n, _ := l["network"].([]any); len(n) != 1 || n[0] != "tcp" {
				t.Errorf("trusttunnel runs HTTP/2 over TCP only: %v", l["network"])
			}
		}},
		{"type: shadowquic\njls-upstream: {addr: www.google.com:443, sni: www.google.com}\nalpn: [h3]\n", false, func(l map[string]any) {
			us, _ := l["users"].([]map[string]any)
			if len(us) != 2 || us[1]["username"] != "s000002" || us[1]["password"] != "x" || l["certificate"] != nil {
				t.Errorf("shadowquic listener: %v", l)
			}
		}},
		{"type: mieru\n", false, func(l map[string]any) {
			us, _ := l["users"].(map[string]string)
			if len(us) != 2 || us["s000001"] != "S3cr3t+/=" || l["transport"] != "TCP" {
				t.Errorf("mieru listener: %v", l)
			}
		}},
		{"type: shadowsocks\ncipher: 2022-blake3-aes-128-gcm\npassword: " + ssKey + "\n", false, func(l map[string]any) {
			// A key per user (EIH): the listener's password is the server's key, each user
			// has one of its own.
			us, _ := l["users"].([]map[string]any)
			own := SSUserKey("2022-blake3-aes-128-gcm", "S3cr3t+/=")
			if l["password"] != ssKey || len(us) != 2 || us[0]["name"] != "s000001" || us[0]["password"] != own || us[1]["password"] == own {
				t.Errorf("shadowsocks listener: %v", l)
			}
			if k, err := base64.StdEncoding.DecodeString(own); err != nil || len(k) != 16 {
				t.Errorf("a user key fits the cipher: %q", own)
			}
		}},
		{"type: shadowsocks\ncipher: 2022-blake3-chacha20-poly1305\npassword: " + ssKey32 + "\n", true, func(l map[string]any) {
			// ChaCha20 has no identity header: one key for everyone.
			if _, has := l["users"]; has {
				t.Errorf("one key, no users: %v", l)
			}
		}},
		{"type: sudoku\nkey: 6f1a44b8-c4e1-4a36-9d62-0b4ddc4c7c4f\naead-method: chacha20-poly1305\npadding-min: 2\npadding-max: 7\ntable-type: prefer_ascii\nenable-pure-downlink: false\n", true, func(l map[string]any) {
			if _, has := l["users"]; has {
				t.Errorf("one key, no users: %v", l)
			}
		}},
		{"type: snell\npsk: p5k-Secret\nversion: 3\n", true, func(l map[string]any) {
			if l["version"] != 3 || l["udp"] != true {
				t.Errorf("snell listener: %v", l)
			}
		}},
	}
	for _, tc := range cases {
		tpl := mustParse(t, tc.src)
		if err := Validate(tpl, Options{}); err != nil {
			t.Fatalf("%s: %v", tc.src, err)
		}
		if Shared(tpl) != tc.shared {
			t.Errorf("%s: shared %v", tc.src, Shared(tpl))
		}
		l, err := Listener(tpl, "n", "", "4443", slots, cert, Options{})
		if err != nil {
			t.Fatalf("%s listener: %v", tc.src, err)
		}
		tc.check(l)
	}
	if v := SnellVersion(mustParse(t, "type: snell\npsk: x\n")); v != 3 {
		t.Errorf("snell without a version speaks %d, want 3", v)
	}
}

func TestExtraTypesRefuseWeakSettings(t *testing.T) {
	for src, want := range map[string]string{
		"type: shadowquic\n": "jls_upstream",
		"type: shadowquic\njls-upstream: {addr: 10.0.0.5:443}\n":                            "jls_upstream_private",
		"type: shadowquic\njls-upstream: {addr: www.google.com:443, proxy: out}\n":          "config_key",
		"type: shadowquic\njls-upstream: {addr: www.google.com:443}\nzero-rtt: true\n":      "config_key",
		"type: mieru\ntransport: UDP\n":                                                     "mieru_transport",
		"type: shadowsocks\ncipher: aes-128-gcm\npassword: x\n":                             "ss_cipher",
		"type: shadowsocks\ncipher: 2022-blake3-aes-256-gcm\npassword: c2hvcnQ=\n":          "ss_password",
		"type: shadowsocks\ncipher: 2022-blake3-aes-128-gcm\npassword: x\nshadow-tls: {}\n": "config_key",
		"type: sudoku\nkey: k\naead-method: none\n":                                         "sudoku_aead",
		"type: sudoku\naead-method: chacha20-poly1305\n":                                    "sudoku_key",
		"type: sudoku\nkey: k\nfallback: 127.0.0.1:80\n":                                    "config_key",
		"type: sudoku\nkey: k\nhttpmask: {mode: ws, path-root: x, fallback: y}\n":           "config_key",
		"type: snell\npsk: \"\"\n":                                                          "snell_psk",
		"type: snell\npsk: x\nversion: 9\n":                                                 "snell_version",
		"type: trusttunnel\nnetwork: [tcp, udp]\n":                                          "config_key",
	} {
		if err := Validate(mustParse(t, src), Options{}); code(err) != want {
			t.Errorf("%q: got %v, want %s", src, err, want)
		}
	}
}

func TestNeeds(t *testing.T) {
	priv, _ := realityKey(t)
	reality := "reality-config: {dest: www.microsoft.com:443, private-key: " + priv + ", short-id: [ab], server-names: [www.microsoft.com]}\n"
	for src, want := range map[string]Needs{
		"type: vless\nxhttp-config: {path: /x, mode: stream-one}\n" + reality: {Type: "vless", Transport: "xhttp"},
		"type: vless\nmikan: {flow: xtls-rprx-vision}\n" + reality:            {Type: "vless", Transport: "tcp"},
		"type: trojan\ngrpc-service-name: g\n" + reality:                      {Type: "trojan", Transport: "grpc"},
		"type: tuic\n":        {Type: "tuic"},
		"type: trusttunnel\n": {Type: "trusttunnel"},
	} {
		if got := NeedsOf(mustParse(t, src)); got != want {
			t.Errorf("%s: %+v, want %+v", strings.SplitN(src, "\n", 2)[0], got, want)
		}
	}
	pq := mustParse(t, "type: vless\nxhttp-config: {path: /x, mode: stream-one}\n"+reality+"decryption: mlkem768x25519plus.native.600s."+testX25519(t)+"\n")
	if n := NeedsOf(pq); !n.Encryption || n.Transport != "xhttp" {
		t.Errorf("pq: %+v", n)
	}
}

func testX25519(t *testing.T) string {
	t.Helper()
	priv, _ := realityKey(t)
	return priv
}
