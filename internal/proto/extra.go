package proto

import (
	"crypto/sha256"
	"encoding/base64"
	"net"
	"net/url"
	"strconv"
	"strings"

	"mikan/internal/netaddr"
)

// Protocols of mihomo's newer cores; only Clash apps (mihomo) and a few others speak them.
//   - TrustTunnel: AdGuard's HTTP/2 tunnel on the node's real TLS certificate.
//   - ShadowQUIC: QUIC that answers anyone without a key as a real site does (JLS).
//   - Mieru: its own encryption with random-looking traffic.
//
// These three check every user, and so does Shadowsocks-2022 with an AES method: the
// server's key and one of each user's (EIH). Sudoku, Snell and Shadowsocks-2022 with
// ChaCha20, which has no identity header, have one key for everyone ("shared"): the node
// cannot tell users apart, so there is no per-user accounting, limit, device binding or
// cut-off, and a leaked key works until it changes.

// ssCiphers are the Shadowsocks-2022 methods and their key sizes; older methods have no
// replay protection and are refused.
var ssCiphers = map[string]int{"2022-blake3-aes-128-gcm": 16, "2022-blake3-aes-256-gcm": 32, "2022-blake3-chacha20-poly1305": 32}

// snellVersion: v3 is what every mihomo client speaks (v4 needs mihomo ≥ 1.19.26). The
// listener would default to v4 and the client to v1, so both get it explicitly.
const snellVersion = 3

// Shared reports whether a listener has one key for all users.
func Shared(t Template) bool {
	if t.Type() == "shadowsocks" {
		return !ssMultiUser(t)
	}
	return rules[t.Type()].shared
}

// ssMultiUser says whether a Shadowsocks-2022 listener has a key per user: the AES methods
// carry the user's identity (EIH), ChaCha20 does not.
func ssMultiUser(t Template) bool { return strings.HasPrefix(t.str("cipher"), "2022-blake3-aes-") }

// ssUserKey is a user's own Shadowsocks-2022 key (uPSK) for the cipher, made from the slot's
// secret: the panel gives it to the user's apps and the node to its listener, and neither
// stores it.
func ssUserKey(cipher, secret string) string {
	sum := sha256.Sum256([]byte("mikan/ss2022/" + secret))
	return base64.StdEncoding.EncodeToString(sum[:ssCiphers[cipher]])
}

func validateExtra(t Template, o Options) error {
	switch t.Type() {
	case "wireguard":
		return validateWireGuard(t, o)
	case "shadowquic":
		up, ok := t["jls-upstream"].(map[string]any)
		if !ok {
			return fail("jls_upstream", "jls-upstream")
		}
		for k := range up {
			if k != "addr" && k != "sni" {
				return fail("config_key", "jls-upstream."+k)
			}
		}
		host, port, err := net.SplitHostPort(str(up["addr"]))
		if p, perr := strconv.Atoi(port); err != nil || host == "" || perr != nil || p < 1 || p > 65535 {
			return fail("jls_upstream", "jls-upstream.addr")
		}
		// The node forwards every unauthenticated packet there: never an internal service.
		if !o.AnyDest && !netaddr.PublicHost(host) {
			return fail("jls_upstream_private", "jls-upstream.addr")
		}
		if net.ParseIP(host) != nil && str(up["sni"]) == "" {
			return fail("jls_upstream", "jls-upstream.sni")
		}
	case "mieru":
		if tr, set := t["transport"]; set && tr != "TCP" {
			return fail("mieru_transport", "transport")
		}
	case "shadowsocks":
		size, ok := ssCiphers[t.str("cipher")]
		if !ok {
			return fail("ss_cipher", "cipher")
		}
		if key, err := base64.StdEncoding.DecodeString(t.str("password")); err != nil || len(key) != size {
			return fail("ss_password", "password")
		}
	case "sudoku":
		if t.str("key") == "" {
			return fail("sudoku_key", "key")
		}
		if m := t.str("aead-method"); t["aead-method"] != nil && m != "chacha20-poly1305" && m != "aes-128-gcm" {
			return fail("sudoku_aead", "aead-method")
		}
		if h, set := t["httpmask"]; set {
			m, ok := h.(map[string]any)
			if !ok {
				return fail("config_key", "httpmask")
			}
			for k := range m {
				if k != "disable" && k != "mode" && k != "path-root" {
					return fail("config_key", "httpmask."+k)
				}
			}
		}
	case "snell":
		if t.str("psk") == "" {
			return fail("snell_psk", "psk")
		}
		if v, set := t["version"]; set {
			if n, ok := toInt(v); !ok || n < 1 || n > 4 {
				return fail("snell_version", "version")
			}
		}
		if raw, set := t["obfs-opts"]; set {
			m, ok := raw.(map[string]any)
			if !ok {
				return fail("config_key", "obfs-opts")
			}
			for k := range m {
				if k != "mode" && k != "host" {
					return fail("config_key", "obfs-opts."+k)
				}
			}
			if mode := str(m["mode"]); mode != "" && mode != "http" && mode != "tls" {
				return fail("config_obfs", "obfs-opts.mode")
			}
		}
	}
	return nil
}

// listenerExtra fills in what the node decides for the newer types.
func listenerExtra(t Template, l map[string]any) {
	switch t.Type() {
	case "trusttunnel":
		l["network"] = []any{"tcp"} // HTTP/2; HTTP/3 would take the UDP port too
	case "mieru":
		l["transport"] = "TCP"
	case "snell":
		// UDP goes inside the Snell stream (v3 on): no UDP port.
		v := snellVersionOf(t)
		l["version"], l["udp"] = v, v >= 3
	}
}

func snellVersionOf(t Template) int {
	if n, ok := toInt(t["version"]); ok && n > 0 {
		return n
	}
	return snellVersion
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		if n == float64(int(n)) {
			return int(n), true
		}
	}
	return 0, false
}

// finishExtra builds the client side of the newer types. Their share links are not a
// common format, so only mihomo profiles carry them; Shadowsocks has an ss:// link.
func (c *clientBuilder) finishExtra() (Client, bool) {
	s := c.in.Slot
	switch c.t.Type() {
	case "wireguard":
		return c.wireGuardClient(), true
	case "trusttunnel":
		c.y["username"], c.y["password"], c.y["udp"] = s.Name, s.Secret, true
		return Client{Mihomo: c.y}, true
	case "shadowquic":
		up := c.t.section("jls-upstream")
		sni := c.ext.Client.SNI
		if sni == "" {
			sni = str(up["sni"])
		}
		if sni == "" {
			sni, _, _ = net.SplitHostPort(str(up["addr"]))
		}
		alpn := strings1(c.t["alpn"])
		if len(alpn) == 0 {
			alpn = []string{"h3"}
		}
		c.y["username"], c.y["password"], c.y["sni"], c.y["alpn"], c.y["udp"] = s.Name, s.Secret, sni, alpn, true
		return Client{Mihomo: c.y}, true
	case "mieru":
		// The Citrus server knows who sends UDP over Mieru (mihomo's did not).
		c.y["transport"], c.y["username"], c.y["password"], c.y["udp"] = "TCP", s.Name, s.Secret, true
		return Client{Mihomo: c.y}, true
	case "shadowsocks":
		cipher, key := c.t.str("cipher"), c.t.str("password")
		if ssMultiUser(c.t) && !c.in.SSOneKey {
			key += ":" + ssUserKey(cipher, s.Secret)
		}
		// UDP goes inside the TCP stream (UDP over TCP, v2): the listener has no UDP port.
		c.y["type"], c.y["cipher"], c.y["password"], c.y["udp"] = "ss", cipher, key, true
		c.y["udp-over-tcp"], c.y["udp-over-tcp-version"] = true, 2
		// SIP002 for 2022 methods: "method:key" percent-encoded, not base64.
		return Client{Mihomo: c.y, URI: "ss://" + url.UserPassword(cipher, key).String() + "@" + c.addr() + "#" + url.PathEscape(c.in.Name)}, true
	case "sudoku":
		c.y["key"] = c.t.str("key")
		c.y["aead-method"] = "chacha20-poly1305"
		if m := c.t.str("aead-method"); m != "" {
			c.y["aead-method"] = m
		}
		for _, k := range []string{"padding-min", "padding-max"} {
			if n, ok := toInt(c.t[k]); ok {
				c.y[k] = n
			}
		}
		if tt := c.t.str("table-type"); tt != "" {
			c.y["table-type"] = tt
		}
		// The downlink mode must match the node's, or the client cannot read its replies.
		if p, ok := c.t["enable-pure-downlink"].(bool); ok {
			c.y["enable-pure-downlink"] = p
		}
		if h := c.t.section("httpmask"); h != nil {
			c.y["httpmask"] = clone(h)
		}
		c.y["udp"] = true
		return Client{Mihomo: c.y}, true
	case "snell":
		// Snell carries UDP from v3 on.
		v := snellVersionOf(c.t)
		c.y["psk"], c.y["version"], c.y["udp"] = c.t.str("psk"), v, v >= 3
		if o := c.t.section("obfs-opts"); o != nil {
			c.y["obfs-opts"] = clone(o)
		}
		return Client{Mihomo: c.y}, true
	}
	return Client{}, false
}

// Needs is what a client app must support to use an inbound.
type Needs struct {
	Type       string // listener type
	Transport  string // vless, trojan: tcp, xhttp, grpc or ws
	Encryption bool   // VLESS Encryption (post-quantum)
	Gecko      bool   // Hysteria2 with Gecko obfuscation
	GetUplink  bool   // XHTTP uploading with GET (for CDNs that drop POST)
}

// NeedsOf describes a template for choosing which apps get it.
func NeedsOf(t Template) Needs {
	n := Needs{Type: t.Type()}
	switch n.Type {
	case "vless", "trojan":
		n.Transport = transport(t)
	}
	n.Encryption = n.Type == "vless" && t.hasEncryption()
	n.Gecko = n.Type == "hysteria2" && t.str("obfs") == ObfsGecko
	if x := t.section("xhttp-config"); x != nil {
		// What the apps send: their own method over the server's.
		m, _ := x["uplink-http-method"].(string)
		if own := scalar(xhttpClient(t)["uplink-http-method"]); own != "" {
			m = own
		}
		n.GetUplink = strings.EqualFold(m, "GET")
	}
	return n
}
