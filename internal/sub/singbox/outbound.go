package singbox

import (
	"fmt"
	"strconv"
	"strings"

	"mikan/internal/proto"
	"mikan/internal/sub/model"
)

// singboxOutbound is p as a sing-box outbound; false for one sing-box cannot carry: a
// protocol of mihomo's own, VLESS Encryption, Gecko, XHTTP, a pinned certificate, a
// Shadowsocks plugin or multiplexing it has no counterpart for.
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
		relay := p.UDPRelay
		if relay == "" {
			relay = "native"
		}
		o["congestion_control"], o["udp_relay_mode"] = p.Congestion, relay
	case "anytls":
		o["type"], o["password"] = "anytls", p.Password
	case "shadowsocks":
		o["type"], o["method"], o["password"] = "shadowsocks", p.Cipher, p.Password
		// A server that takes UDP over TCP alone listens on TCP: raw UDP would go nowhere.
		if p.UDPOverTCP > 0 {
			o["udp_over_tcp"] = map[string]any{"enabled": true, "version": p.UDPOverTCP}
		}
		if p.Plugin != "" {
			plugin, opts, ok := sip003(p.Plugin, p.PluginOpts)
			if !ok {
				return nil, false
			}
			o["plugin"], o["plugin_opts"] = plugin, opts
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
			h := map[string]any{}
			for k, v := range tr.Headers {
				h[k] = v
			}
			if tr.Host != "" {
				h["Host"] = tr.Host
			}
			if len(h) > 0 {
				t["headers"] = h
			}
			o["transport"] = t
		case "grpc":
			o["transport"] = map[string]any{"type": "grpc", "service_name": tr.Service, "idle_timeout": grpcIdleTimeout, "ping_timeout": grpcPingTimeout}
		default:
			return nil, false // XHTTP
		}
	}
	if p.Smux != nil {
		mux, ok := multiplex(p)
		if !ok {
			return nil, false
		}
		o["multiplex"] = mux
	}
	if !p.UDP && p.Type != "tuic" && p.Type != "hysteria2" {
		o["network"] = "tcp"
	}
	return o, true
}

// sip003 is a Shadowsocks plugin of mihomo's as sing-box runs it: its name and its options
// in SIP003's form (key=value;...); false for a plugin sing-box has none of (shadow-tls,
// restls, gost-plugin, kcptun, ...) or a setting it cannot take. The hosts are mihomo's
// default when none is given, which sing-box's are not.
func sip003(plugin string, opts map[string]any) (string, string, bool) {
	var args []string
	arg := func(k string, v any) {
		if v == nil {
			args = append(args, k)
			return
		}
		args = append(args, k+"="+sip003Escaper.Replace(str(v)))
	}
	host := str(opts["host"])
	if host == "" {
		host = "bing.com"
	}
	switch plugin {
	case "obfs":
		mode := str(opts["mode"])
		if mode != "http" && mode != "tls" {
			return "", "", false // mihomo refuses it
		}
		arg("obfs", mode)
		arg("obfs-host", host)
		return "obfs-local", strings.Join(args, ";"), true
	case "v2ray-plugin":
		// sing-box's v2ray-plugin sends Host alone and checks the certificate as any
		// website's: no other headers, skip-cert-verify, pinning, ECH or HTTPUpgrade.
		for k, v := range opts {
			switch k {
			case "mode", "host", "path", "tls", "mux":
			default:
				if !unset(v) {
					return "", "", false
				}
			}
		}
		if mode := str(opts["mode"]); mode != "websocket" {
			return "", "", false // mihomo's only mode
		}
		arg("mode", "websocket")
		if opts["tls"] == true {
			arg("tls", nil)
		}
		arg("host", host)
		if path := str(opts["path"]); path != "" {
			arg("path", path)
		}
		if opts["mux"] == false {
			arg("mux", 0) // mihomo multiplexes unless told not to, as sing-box does
		}
		return "v2ray-plugin", strings.Join(args, ";"), true
	}
	return "", "", false
}

// sip003Escaper escapes what SIP003 options take as their own.
var sip003Escaper = strings.NewReplacer(`\`, `\\`, `=`, `\=`, `;`, `\;`)

// unset: v is a setting left at its zero (ech-opts: {enabled: false}).
func unset(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case bool:
		return !x
	case string:
		return x == ""
	case int:
		return x == 0
	case []any:
		return len(x) == 0
	case map[string]any:
		for _, y := range x {
			if !unset(y) {
				return false
			}
		}
		return true
	}
	return false
}

// multiplex is the sing-box multiplex of p's smux; false where sing-box has none: a
// protocol but VLESS, Trojan and Shadowsocks, UDP kept out of it (only-tcp), a TCP Brutal
// speed but whole Mbps.
func multiplex(p model.Proxy) (map[string]any, bool) {
	m := p.Smux
	switch p.Type {
	case "vless", "trojan", "shadowsocks":
	default:
		return nil, false
	}
	if m.OnlyTCP {
		return nil, false
	}
	o := map[string]any{"enabled": true}
	switch m.Protocol {
	case "":
	case "smux", "yamux", "h2mux":
		o["protocol"] = m.Protocol
	default:
		return nil, false
	}
	for k, n := range map[string]int{"max_connections": m.MaxConnections, "min_streams": m.MinStreams, "max_streams": m.MaxStreams} {
		if n > 0 {
			o[k] = n
		}
	}
	if m.Padding {
		o["padding"] = true
	}
	if b := m.Brutal; b != nil {
		up, ok1 := mbps(b.Up)
		down, ok2 := mbps(b.Down)
		if !ok1 || !ok2 {
			return nil, false
		}
		o["brutal"] = map[string]any{"enabled": true, "up_mbps": up, "down_mbps": down}
	}
	return o, true
}

// mbps is a speed of mihomo's in whole Mbps: a bare number, or one of Mbps.
func mbps(s string) (int, bool) {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "Mbps"))
	n, err := strconv.Atoi(s)
	return n, err == nil && n > 0
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
