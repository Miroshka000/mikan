package subtest

import (
	"mikan/internal/proto"
	"mikan/internal/sub/apps"
	"mikan/internal/sub/build"
	"mikan/internal/sub/model"
)

// AllTypes are one inbound of each kind the default presets leave out, on the node: TLS of
// the node's certificate, WebSocket, Gecko's obfuscation, Shadowsocks-2022 and the
// protocols of mihomo's newer cores. A core that has no word for one leaves it out.
func AllTypes(node, firstID int64) []model.Inbound {
	return []model.Inbound{
		Inbound(firstID, node, "VLESS TLS", 443, proto.Template{"type": "vless", "mikan": map[string]any{"tls": "node"}}),
		Inbound(firstID+1, node, "VLESS WS", 2053, proto.Template{"type": "vless", "ws-path": "/golden-ws", "mikan": map[string]any{"tls": "node"}}),
		Inbound(firstID+2, node, "Hysteria2 Gecko", 8443, proto.Template{"type": "hysteria2", "obfs": "gecko", "obfs-password": "golden-gecko-password", "obfs-min-packet-size": 600, "obfs-max-packet-size": 1100}),
		// The server's key is 16 bytes: the user's own goes next to it.
		Inbound(firstID+3, node, "Shadowsocks 2022", 8388, proto.Template{"type": "shadowsocks", "cipher": "2022-blake3-aes-128-gcm", "password": "AAECAwQFBgcICQoLDA0ODw=="}),
		Inbound(firstID+4, node, "TrustTunnel", 4443, proto.Template{"type": "trusttunnel", "congestion-controller": "bbr"}),
		Inbound(firstID+5, node, "ShadowQUIC", 4444, proto.Template{"type": "shadowquic", "jls-upstream": map[string]any{"addr": "www.google.com:443", "sni": "www.google.com"}, "alpn": []any{"h3"}}),
		Inbound(firstID+6, node, "Mieru", 2999, proto.Template{"type": "mieru"}),
		Inbound(firstID+7, node, "Sudoku", 3000, proto.Template{"type": "sudoku", "key": "6f1a44b8-c4e1-4a36-9d62-0b4ddc4c7c4f", "aead-method": "chacha20-poly1305", "padding-min": 2, "padding-max": 7, "table-type": "prefer_ascii", "enable-pure-downlink": false}),
		Inbound(firstID+8, node, "Snell", 3001, proto.Template{"type": "snell", "psk": "p5k-Secret", "version": 3}),
	}
}

// EveryType is a user of one node with a domain and a public certificate, given AllTypes.
func EveryType() User {
	return User{
		Build: build.Profile{Keys: Keys, Inbounds: AllTypes(1, 1),
			Nodes: []model.Node{{ID: 1, Endpoint: model.Endpoint{Host: "vpn.example.com", SNI: "vpn.example.com"}}}},
		Direct: []string{"vpn.example.com"},
	}
}

// EveryTypeVariant is the user of EveryType with no app, so that what each core drops is
// its own doing: its golden files are of the mode All alone.
var EveryTypeVariant = Variant{"every-type", EveryType, false, apps.App{}, model.GroupNames{}}
