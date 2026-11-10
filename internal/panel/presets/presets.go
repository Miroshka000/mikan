// Package presets is the catalog of ready inbounds: each preset generates a listener
// template (see internal/proto) with fresh keys, paths and passwords.
package presets

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"strings"

	"mikan/internal/panel/secure"
	"mikan/internal/proto"
)

type Info struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Summary string `json:"summary"` // Russian fallback; the UI translates presets.<id>
	Type    string `json:"type"`    // mihomo listener type
	Network string `json:"network"` // tcp | udp
	Port    string `json:"default_port"`
	Name    string `json:"default_name"`
	SubName string `json:"sub_name"` // proxy name in subscriptions unless the admin sets one
	Default bool   `json:"default"`  // created on a fresh install
	// Apps: "mihomo" when only Clash apps (on the mihomo core) can use it; they alone get
	// it in their subscription. Empty: most apps.
	Apps string `json:"apps,omitempty"`
	// Shared: one key for everyone. The node cannot tell users apart: no per-user traffic,
	// limits, device binding or cut-off; a leaked key works until the key changes.
	Shared bool `json:"shared,omitempty"`
	// DomainCert: the point is a real certificate, which only a node with a domain has (the
	// panel's node) or one with its own; elsewhere it runs on a pinned self-signed one.
	DomainCert bool `json:"domain_cert,omitempty"`
	// NestedTLS: the TLS of the sites a client opens goes inside this protocol's own TLS
	// as it is (TLS in TLS, Trojan and VLESS without Vision): the sizes of its first records
	// can give the proxy away to DPI. Vision and AnyTLS hide them.
	NestedTLS bool `json:"nested_tls,omitempty"`
}

// Custom is the "own config" entry: the admin writes the template in the editor.
const Custom = "custom"

// PresetPQ is XHTTP with VLESS Encryption (post-quantum) on top of REALITY. Not a default:
// sing-box based apps cannot speak it and fail on this inbound.
const PresetPQ = "vless_reality_xhttp_pq"

// PresetGecko is Hysteria2 with Gecko obfuscation (proto.ObfsGecko): only mihomo apps
// that name a core of 1.19.26 or later get it, the rest go on with plain Hysteria2.
const PresetGecko = "hysteria2_gecko"

// PresetTLSXHTTP and PresetTLSVision are VLESS on plain TLS with the node certificate
// (mikan.tls: node): on the panel's node with a domain it is the panel's public one, so a
// probe sees the same site as on the panel's port; on a node with an own certificate,
// that one; otherwise a self-signed one, pinned in links.
const (
	PresetTLSXHTTP  = "vless_tls_xhttp"
	PresetTLSVision = "vless_tls_vision"
	// PresetTLSWS is VLESS over WebSocket on TLS: the transport some mobile operators let
	// through when nothing else passes, and the one a CDN (Cloudflare, Yandex) carries to
	// the node. Behind a CDN its client address is the CDN's (inbound "behind a proxy").
	PresetTLSWS = "vless_tls_ws"
	// PresetCDNXHTTP is XHTTP in packet-up for a CDN in front of the node: plain requests a
	// CDN passes on and caches nothing of. PresetCDNGet uploads with GET, for CDNs that
	// refuse POST to the origin (Yandex Cloud CDN and others): the data go in a header.
	PresetCDNXHTTP = "vless_cdn_xhttp"
	PresetCDNGet   = "vless_cdn_xhttp_get"
)

// Order is display and fallback order. Since 2026 the RU DPI freezes a server's 443/tcp
// after bursts of parallel TLS handshakes; Vision opens one handshake per app connection,
// so 443/tcp goes to XHTTP, which clients multiplex over a few long-lived connections.
var All = []Info{
	{ID: "vless_reality_xhttp", Title: "VLESS · REALITY · XHTTP", Summary: "Основной для РФ: похож на обычный HTTPS, держит мало соединений", Type: "vless", Network: "tcp", Port: "443", Name: "vless-xhttp", SubName: "VLESS XHTTP", Default: true, NestedTLS: true},
	{ID: "hysteria2", Title: "Hysteria2", Summary: "Быстрый на плохих каналах, работает по UDP", Type: "hysteria2", Network: "udp", Port: "443", Name: "hysteria2", SubName: "Hysteria2", Default: true},
	{ID: PresetGecko, Title: "Hysteria2 · Gecko", Summary: "Hysteria2, у которого рукопожатие режется на куски случайного размера: против DPI, который узнаёт QUIC по размерам пакетов. Только приложения на ядре mihomo 1.19.26+", Type: "hysteria2", Network: "udp", Port: "2443", Name: "hysteria2-gecko", SubName: "Hysteria2 Gecko", Apps: "mihomo"},
	{ID: "tuic_v5", Title: "TUIC v5", Summary: "Альтернатива на QUIC, тоже по UDP", Type: "tuic", Network: "udp", Port: "8443", Name: "tuic", SubName: "TUIC", Default: true},
	{ID: "vless_reality_vision", Title: "VLESS · REALITY · Vision", Summary: "Для старых клиентов без XHTTP. На 443 в РФ быстро замораживается", Type: "vless", Network: "tcp", Port: "8443", Name: "vless-vision", SubName: "VLESS Vision", Default: true},
	{ID: "vless_reality_grpc", Title: "VLESS · REALITY · gRPC", Summary: "HTTP/2 с мультиплексом: мало соединений, другой рисунок трафика", Type: "vless", Network: "tcp", Port: "2053", Name: "vless-grpc", SubName: "VLESS gRPC", NestedTLS: true},
	{ID: "trojan_reality", Title: "Trojan · REALITY", Summary: "Другой протокол под той же маскировкой — запасной вариант", Type: "trojan", Network: "tcp", Port: "2087", Name: "trojan", SubName: "Trojan", NestedTLS: true},
	{ID: PresetTLSXHTTP, Title: "VLESS · TLS · XHTTP", Summary: "Обычный TLS на настоящем сертификате домена панели, как у сайта: на чужие запросы отвечает как веб-сервер, держит мало соединений", Type: "vless", Network: "tcp", Port: "2443", Name: "vless-tls-xhttp", SubName: "VLESS TLS XHTTP", DomainCert: true, NestedTLS: true},
	{ID: PresetTLSVision, Title: "VLESS · TLS · Vision", Summary: "Классический VLESS Vision на настоящем сертификате домена панели: работает почти во всех приложениях. На 443 в РФ быстро замораживается", Type: "vless", Network: "tcp", Port: "3443", Name: "vless-tls-vision", SubName: "VLESS TLS Vision", DomainCert: true},
	{ID: PresetTLSWS, Title: "VLESS · TLS · WebSocket", Summary: "WebSocket на настоящем сертификате: проходит у операторов, где работает только WS, и через CDN (Cloudflare, Яндекс). Для CDN заполните блок «За CDN»", Type: "vless", Network: "tcp", Port: "2053", Name: "vless-tls-ws", SubName: "VLESS WS", DomainCert: true, NestedTLS: true},
	{ID: PresetCDNXHTTP, Title: "CDN · VLESS · XHTTP", Summary: "Через CDN (Cloudflare, Яндекс, VK и другие): клиенты подключаются к домену CDN, CDN — к ноде. Адрес CDN и настоящий IP клиентов задаются в блоке «За CDN»", Type: "vless", Network: "tcp", Port: "2083", Name: "vless-cdn", SubName: "VLESS CDN", DomainCert: true, NestedTLS: true},
	{ID: PresetCDNGet, Title: "CDN · VLESS · XHTTP · GET", Summary: "Для CDN, которые не пропускают POST к серверу: отправка запросами GET, данные в заголовке. Нужен mihomo 1.19.32+ или свежий Xray у клиентов", Type: "vless", Network: "tcp", Port: "2087", Name: "vless-cdn-get", SubName: "VLESS CDN GET", DomainCert: true, NestedTLS: true},
	{ID: "anytls", Title: "AnyTLS", Summary: "TLS с паддингом против анализа размеров пакетов; нужен клиент на mihomo или sing-box", Type: "anytls", Network: "tcp", Port: "2083", Name: "anytls", SubName: "AnyTLS"},
	{ID: PresetPQ, Title: "VLESS · REALITY · XHTTP · PQ", Summary: "XHTTP с постквантовым шифрованием VLESS: записанный сейчас трафик не расшифровать и в будущем. Нужен свежий клиент на mihomo или Xray; приложения на sing-box не подключатся", Type: "vless", Network: "tcp", Port: "2096", Name: "vless-pq", SubName: "VLESS PQ", NestedTLS: true},
	{ID: "trusttunnel", Title: "TrustTunnel", Summary: "Протокол AdGuard: HTTP/2 на настоящем сертификате ноды. На запрос без ключа отвечает 407, как прокси, и активный сканер его узнаёт: не ставьте его на ноду, IP которой важно сберечь", Type: "trusttunnel", Network: "tcp", Port: "4443", Name: "trusttunnel", SubName: "TrustTunnel", Apps: "mihomo"},
	{ID: "shadowquic", Title: "ShadowQUIC", Summary: "QUIC, который на чужие запросы отвечает как настоящий сайт (JLS). Запасной протокол по UDP", Type: "shadowquic", Network: "udp", Port: "4443", Name: "shadowquic", SubName: "ShadowQUIC", Apps: "mihomo"},
	{ID: "mieru", Title: "Mieru", Summary: "Своё шифрование и случайный рисунок трафика. Только TCP", Type: "mieru", Network: "tcp", Port: "5443", Name: "mieru", SubName: "Mieru", Apps: "mihomo"},
	{ID: "shadowsocks_2022", Title: "Shadowsocks-2022", Summary: "Классика, быстрый. У каждого пользователя свой ключ", Type: "shadowsocks", Network: "tcp", Port: "6443", Name: "shadowsocks", SubName: "Shadowsocks"},
	{ID: "sudoku", Title: "Sudoku", Summary: "Прячет трафик под HTTP и текст. Один ключ на всех пользователей", Type: "sudoku", Network: "tcp", Port: "7443", Name: "sudoku", SubName: "Sudoku", Apps: "mihomo", Shared: true},
	{ID: "snell", Title: "Snell", Summary: "Протокол Surge. Один ключ на всех пользователей", Type: "snell", Network: "tcp", Port: "9443", Name: "snell", SubName: "Snell", Apps: "mihomo", Shared: true},
	{ID: Custom, Title: "Свой конфиг", Summary: "Шаблон листенера mihomo в редакторе: любой поддерживаемый тип и параметры", Network: "", Port: "", Name: "custom", SubName: "Custom"},
}

func Get(id string) (Info, bool) {
	for _, p := range All {
		if p.ID == id {
			return p, true
		}
	}
	return Info{}, false
}

// DefaultDest is the REALITY target used until the admin picks one.
const DefaultDest = "www.microsoft.com:443"

// QUICDest is where ShadowQUIC sends anyone without a key: a site that answers over
// HTTP/3, so a probe sees a real QUIC server.
const QUICDest = "www.google.com:443"

// NewConfig generates a template with fresh keys for a preset. dest is the REALITY
// target ("" = DefaultDest); other presets ignore it.
func NewConfig(id, dest string) (string, error) {
	if dest == "" {
		dest = DefaultDest
	}
	var t proto.Template
	switch id {
	case "vless_reality_xhttp":
		t = proto.Template{"type": "vless", "xhttp-config": map[string]any{"path": randomPath(), "mode": "stream-one"}}
	case PresetPQ:
		key, err := newX25519()
		if err != nil {
			return "", err
		}
		// 600s: how long a 0-RTT ticket would live; our clients do a full handshake anyway.
		t = proto.Template{"type": "vless", "xhttp-config": map[string]any{"path": randomPath(), "mode": "stream-one"},
			"decryption": proto.VLESSEncMethod + ".native.600s." + key}
	case "vless_reality_vision":
		t = proto.Template{"type": "vless", "mikan": map[string]any{"flow": "xtls-rprx-vision"}}
	case "vless_reality_grpc":
		t = proto.Template{"type": "vless", "grpc-service-name": strings.ToLower(secure.Token(8))}
	case "trojan_reality":
		t = proto.Template{"type": "trojan"}
	case PresetTLSXHTTP:
		return proto.Marshal(proto.Template{"type": "vless", "xhttp-config": map[string]any{"path": randomPath(), "mode": "stream-one"},
			"mikan": map[string]any{"tls": "node"}}), nil
	case PresetTLSVision:
		return proto.Marshal(proto.Template{"type": "vless", "mikan": map[string]any{"flow": "xtls-rprx-vision", "tls": "node"}}), nil
	case PresetTLSWS:
		return proto.Marshal(proto.Template{"type": "vless", "ws-path": randomPath(), "mikan": map[string]any{"tls": "node"}}), nil
	case PresetCDNXHTTP:
		return proto.Marshal(proto.Template{"type": "vless", "xhttp-config": map[string]any{"path": randomPath(), "mode": "packet-up"},
			"mikan": map[string]any{"tls": "node"}}), nil
	case PresetCDNGet:
		return proto.Marshal(proto.Template{"type": "vless", "xhttp-config": map[string]any{"path": randomPath(), "mode": "packet-up",
			"uplink-http-method": "GET", "uplink-data-placement": "header", "uplink-data-key": "X-Data"},
			"mikan": map[string]any{"tls": "node"}}), nil
	case "hysteria2":
		return proto.Marshal(proto.Template{"type": "hysteria2", "alpn": []any{"h3"}, "obfs": proto.ObfsSalamander, "obfs-password": secure.Token(24)}), nil
	case PresetGecko:
		return proto.Marshal(proto.Template{"type": "hysteria2", "alpn": []any{"h3"}, "obfs": proto.ObfsGecko, "obfs-password": secure.Token(24)}), nil
	case "tuic_v5":
		return proto.Marshal(proto.Template{"type": "tuic", "alpn": []any{"h3"}, "congestion-controller": "bbr", "max-idle-time": 15000, "authentication-timeout": 1000}), nil
	case "anytls":
		return proto.Marshal(proto.Template{"type": "anytls"}), nil
	case "trusttunnel":
		return proto.Marshal(proto.Template{"type": "trusttunnel", "congestion-controller": "bbr"}), nil
	case "shadowquic":
		host, _, _ := net.SplitHostPort(QUICDest)
		return proto.Marshal(proto.Template{"type": "shadowquic", "jls-upstream": map[string]any{"addr": QUICDest, "sni": host}, "alpn": []any{"h3"},
			"congestion-controller": "bbr"}), nil
	case "mieru":
		return proto.Marshal(proto.Template{"type": "mieru", "transport": "TCP"}), nil
	case "shadowsocks_2022":
		key := make([]byte, 16)
		if _, err := rand.Read(key); err != nil {
			return "", err
		}
		return proto.Marshal(proto.Template{"type": "shadowsocks", "cipher": "2022-blake3-aes-128-gcm", "password": base64.StdEncoding.EncodeToString(key)}), nil
	case "sudoku":
		return proto.Marshal(proto.Template{"type": "sudoku", "key": secure.Token(32), "aead-method": "chacha20-poly1305", "padding-min": 2, "padding-max": 7,
			"table-type": "prefer_ascii"}), nil
	case "snell":
		return proto.Marshal(proto.Template{"type": "snell", "psk": secure.Token(32), "version": 3}), nil
	case Custom:
		return "", fmt.Errorf("the custom preset takes the admin's config")
	default:
		return "", fmt.Errorf("unknown preset %q", id)
	}
	r, err := NewReality(dest)
	if err != nil {
		return "", err
	}
	t["reality-config"] = r
	return proto.Marshal(t), nil
}

func randomPath() string { return "/" + strings.ToLower(secure.Token(10)) }

// newX25519 is a fresh X25519 private key, clamped like Xray's and mihomo's generators so
// equivalent keys never occur, as raw base64url.
func newX25519() (string, error) {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return "", err
	}
	k[0] &= 248
	k[31] &= 127
	k[31] |= 64
	return base64.RawURLEncoding.EncodeToString(k), nil
}

// NewReality returns a reality-config section with a fresh X25519 key and short id.
func NewReality(dest string) (map[string]any, error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	sid := make([]byte, 4)
	if _, err := rand.Read(sid); err != nil {
		return nil, err
	}
	host, _, err := net.SplitHostPort(dest)
	if err != nil {
		host = dest
	}
	return map[string]any{
		"dest": dest, "server-names": []any{host},
		"private-key": base64.RawURLEncoding.EncodeToString(k.Bytes()), "short-id": []any{hex.EncodeToString(sid)},
	}, nil
}

// SetDest points a template's REALITY camouflage at another site. sni is the name
// clients send; "" means the host of dest (a picked neighbor has dest = its IP instead).
func SetDest(t proto.Template, dest, sni string) error {
	r, ok := t["reality-config"].(map[string]any)
	if !ok {
		return &proto.Error{Code: "dest_no_reality", Field: "reality-config"}
	}
	host, port, err := net.SplitHostPort(dest)
	if err != nil || host == "" || port == "" {
		return &proto.Error{Code: "dest_format", Field: "reality-config.dest"}
	}
	if sni == "" {
		if net.ParseIP(host) != nil {
			return &proto.Error{Code: "reality_sni", Field: "reality-config.server-names"}
		}
		sni = host
	}
	r["dest"], r["server-names"] = dest, []any{sni}
	return nil
}

// SetServerNames keeps a template's REALITY target and changes the names it is shown under:
// links carry the first, the node lets in clients that send any of them.
func SetServerNames(t proto.Template, names []string) error {
	r, ok := t["reality-config"].(map[string]any)
	if !ok {
		return &proto.Error{Code: "dest_no_reality", Field: "reality-config"}
	}
	list := make([]any, 0, len(names))
	for _, n := range names {
		list = append(list, n)
	}
	r["server-names"] = list
	return nil
}

// Dest reads the REALITY target of a template ("" when there is none).
func Dest(t proto.Template) (dest string, serverNames []string) {
	r, ok := t["reality-config"].(map[string]any)
	if !ok {
		return "", nil
	}
	dest, _ = r["dest"].(string)
	if names, ok := r["server-names"].([]any); ok {
		for _, n := range names {
			if s, ok := n.(string); ok {
				serverNames = append(serverNames, s)
			}
		}
	}
	return dest, serverNames
}

// Rekey gives a copy of an inbound keys of its own, so the copy and the original never
// share a secret: a fresh REALITY key and short id (the target stays), a new Hysteria2
// obfuscation password, VLESS Encryption key, Shadowsocks password, Sudoku key or Snell
// PSK. Paths and the rest of the settings stay as they were.
func Rekey(t proto.Template) error {
	if r, ok := t["reality-config"].(map[string]any); ok {
		fresh, err := NewReality(DefaultDest)
		if err != nil {
			return err
		}
		r["private-key"], r["short-id"] = fresh["private-key"], fresh["short-id"]
	}
	if d, ok := t["decryption"].(string); ok && strings.HasPrefix(d, proto.VLESSEncMethod+".") {
		key, err := newX25519()
		if err != nil {
			return err
		}
		if i := strings.LastIndexByte(d, '.'); i > 0 {
			t["decryption"] = d[:i+1] + key
		}
	}
	switch t.Type() {
	case "hysteria2":
		if _, ok := t["obfs-password"]; ok {
			t["obfs-password"] = secure.Token(24)
		}
	case "shadowsocks":
		key := make([]byte, 16)
		if old, err := base64.StdEncoding.DecodeString(str(t["password"])); err == nil && len(old) > 0 {
			key = make([]byte, len(old)) // 2022 ciphers take a key of their own length
		}
		if _, err := rand.Read(key); err != nil {
			return err
		}
		t["password"] = base64.StdEncoding.EncodeToString(key)
	case "sudoku":
		t["key"] = secure.Token(32)
	case "snell":
		t["psk"] = secure.Token(32)
	}
	return nil
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
