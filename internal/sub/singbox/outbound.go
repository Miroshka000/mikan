package singbox

import (
	"fmt"
	"strconv"
	"strings"
)

// singboxOutbound is a mihomo proxy as a sing-box outbound; false for one sing-box cannot
// carry.
func singboxOutbound(m map[string]any) (map[string]any, bool) {
	typ := str(m["type"])
	o := map[string]any{"tag": str(m["name"]), "server": str(m["server"]), "server_port": m["port"]}
	tls, ok := singboxTLS(m)
	if !ok {
		return nil, false
	}
	switch typ {
	case "vless":
		o["type"], o["uuid"], o["packet_encoding"] = "vless", str(m["uuid"]), "xudp"
		if f := str(m["flow"]); f != "" {
			o["flow"] = f
		}
		if m["encryption"] != nil && str(m["encryption"]) != "none" {
			return nil, false
		}
	case "trojan":
		o["type"], o["password"] = "trojan", str(m["password"])
	case "hysteria2":
		o["type"], o["password"] = "hysteria2", str(m["password"])
		if ports := str(m["ports"]); ports != "" {
			var list []string
			for _, p := range strings.Split(ports, ",") {
				list = append(list, strings.ReplaceAll(strings.TrimSpace(p), "-", ":"))
			}
			o["server_ports"] = list
			delete(o, "server_port")
		}
		switch str(m["obfs"]) {
		case "":
		case "salamander":
			o["obfs"] = map[string]any{"type": "salamander", "password": str(m["obfs-password"])}
		default:
			return nil, false // Gecko is mihomo's own
		}
	case "tuic":
		o["type"], o["uuid"], o["password"] = "tuic", str(m["uuid"]), str(m["password"])
		o["congestion_control"], o["udp_relay_mode"] = str(m["congestion-controller"]), str(m["udp-relay-mode"])
	case "anytls":
		o["type"], o["password"] = "anytls", str(m["password"])
	case "ss":
		o["type"], o["method"], o["password"] = "shadowsocks", str(m["cipher"]), str(m["password"])
	default:
		return nil, false
	}
	if tls != nil {
		o["tls"] = tls
	}
	switch str(m["network"]) {
	case "", "tcp":
	case "ws":
		w := anyMap(m["ws-opts"])
		t := map[string]any{"type": "ws", "path": str(w["path"])}
		if h := anyMap(w["headers"]); len(h) > 0 {
			t["headers"] = h
		}
		o["transport"] = t
	case "grpc":
		o["transport"] = map[string]any{"type": "grpc", "service_name": str(anyMap(m["grpc-opts"])["grpc-service-name"]), "idle_timeout": grpcIdleTimeout, "ping_timeout": grpcPingTimeout}
	default:
		return nil, false // XHTTP, HTTP/2 and the like
	}
	if m["udp"] == false && typ != "tuic" && typ != "hysteria2" {
		o["network"] = "tcp"
	}
	return o, true
}

// singboxTLS is the tls block of a proxy, nil for none; false for a pinned self-signed
// certificate, which sing-box 1.12 cannot pin.
func singboxTLS(m map[string]any) (map[string]any, bool) {
	typ := str(m["type"])
	reality := anyMap(m["reality-opts"])
	if m["tls"] != true && reality == nil && typ != "hysteria2" && typ != "tuic" && typ != "anytls" {
		return nil, true
	}
	if str(m["fingerprint"]) != "" {
		return nil, false
	}
	t := map[string]any{"enabled": true}
	sni := str(m["servername"])
	if sni == "" {
		sni = str(m["sni"])
	}
	if sni != "" {
		t["server_name"] = sni
	}
	if alpn := anyList(m["alpn"]); len(alpn) > 0 {
		t["alpn"] = alpn
	}
	if m["skip-cert-verify"] == true {
		t["insecure"] = true
	}
	fp := str(m["client-fingerprint"])
	if reality != nil {
		t["reality"] = map[string]any{"enabled": true, "public_key": str(reality["public-key"]), "short_id": str(reality["short-id"])}
		if fp == "" {
			fp = "chrome" // REALITY needs uTLS in sing-box
		}
	}
	if fp != "" && typ != "hysteria2" && typ != "tuic" {
		t["utls"] = map[string]any{"enabled": true, "fingerprint": fp}
	}
	return t, true
}

// singboxGroups are the Clash groups as selectors and url tests. A member sing-box has no

func anyList(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case []string:
		return anys(x)
	}
	return nil
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
