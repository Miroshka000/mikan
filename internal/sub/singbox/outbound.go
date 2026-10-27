package singbox

import (
	"fmt"
	"strconv"
	"strings"

	"mikan/internal/proto"
	"mikan/internal/sub/model"
)

// singboxOutbound is p as a sing-box outbound; false for one sing-box cannot carry: a
// protocol of mihomo's own, VLESS Encryption, Gecko, XHTTP, a pinned certificate.
func singboxOutbound(p model.Proxy) (map[string]any, bool) {
	o := map[string]any{"tag": p.Name, "server": p.Server, "server_port": p.Port}
	tls, ok := singboxTLS(p)
	if !ok {
		return nil, false
	}
	switch p.Type {
	case "vless":
		o["type"], o["uuid"], o["packet_encoding"] = "vless", p.UUID, "xudp"
		if p.Flow != "" {
			o["flow"] = p.Flow
		}
		if p.Encryption != "" {
			return nil, false
		}
	case "trojan":
		o["type"], o["password"] = "trojan", p.Password
	case "hysteria2":
		o["type"], o["password"] = "hysteria2", p.Password
		if p.Ports != "" {
			var list []string
			for _, r := range strings.Split(p.Ports, ",") {
				list = append(list, strings.ReplaceAll(strings.TrimSpace(r), "-", ":"))
			}
			o["server_ports"] = list
			delete(o, "server_port")
		}
		if ob := p.Obfs; ob != nil {
			if ob.Type != "salamander" {
				return nil, false // Gecko is mihomo's own
			}
			o["obfs"] = map[string]any{"type": "salamander", "password": ob.Password}
		}
	case "tuic":
		o["type"], o["uuid"], o["password"] = "tuic", p.UUID, p.Password
		o["congestion_control"], o["udp_relay_mode"] = p.Congestion, "native"
	case "anytls":
		o["type"], o["password"] = "anytls", p.Password
	case "shadowsocks":
		o["type"], o["method"], o["password"] = "shadowsocks", p.Cipher, p.Password
		// A server that takes UDP over TCP alone listens on TCP: raw UDP would go nowhere.
		if p.UDPOverTCP > 0 {
			o["udp_over_tcp"] = map[string]any{"enabled": true, "version": p.UDPOverTCP}
		}
	default:
		return nil, false
	}
	if tls != nil {
		o["tls"] = tls
	}
	if tr := p.Transport; tr != nil {
		switch tr.Network {
		case "tcp":
		case "ws":
			t := map[string]any{"type": "ws", "path": tr.Path}
			if tr.Host != "" {
				t["headers"] = map[string]any{"Host": tr.Host}
			}
			o["transport"] = t
		case "grpc":
			o["transport"] = map[string]any{"type": "grpc", "service_name": tr.Service, "idle_timeout": grpcIdleTimeout, "ping_timeout": grpcPingTimeout}
		default:
			return nil, false // XHTTP
		}
	}
	if !p.UDP && p.Type != "tuic" && p.Type != "hysteria2" {
		o["network"] = "tcp"
	}
	return o, true
}

// singboxTLS is the tls block of p, nil for none; false for a pinned self-signed
// certificate, which sing-box 1.12 cannot pin.
func singboxTLS(p model.Proxy) (map[string]any, bool) {
	tls := p.TLS
	if tls == nil {
		if p.Type != "hysteria2" && p.Type != "tuic" && p.Type != "anytls" {
			return nil, true
		}
		tls = &model.TLS{} // TLS by themselves
	}
	if tls.Pin != "" {
		return nil, false
	}
	t := map[string]any{"enabled": true}
	if tls.SNI != "" {
		t["server_name"] = tls.SNI
	}
	if len(p.ALPN) > 0 {
		t["alpn"] = p.ALPN
	}
	if tls.Insecure {
		t["insecure"] = true
	}
	fp := tls.Fingerprint
	if r := tls.Reality; r != nil {
		t["reality"] = map[string]any{"enabled": true, "public_key": r.PublicKey, "short_id": r.ShortID}
		if fp == "" {
			fp = proto.DefaultFingerprint // REALITY needs uTLS in sing-box
		}
	}
	if fp != "" && p.Type != "hysteria2" && p.Type != "tuic" {
		t["utls"] = map[string]any{"enabled": true, "fingerprint": fp}
	}
	return t, true
}

func anyMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case int:
		return strconv.Itoa(x)
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}
