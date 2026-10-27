package mihomo

import (
	"strconv"
	"strings"

	"mikan/internal/sub/model"
)

// ReadProxy is a proxy of an admin's own Clash profile in the model, for a core that writes
// the model its own way (sing-box): the template's own proxies go there next to the
// panel's. It reads what Proxy writes, of the protocols other cores speak too (VLESS,
// Trojan, Hysteria2, TUIC, AnyTLS, Shadowsocks); false for one the model cannot hold: a
// protocol of mihomo's own, a Shadowsocks plugin or smux, UDP over TCP of a version mihomo
// refuses, a transport but TCP, WebSocket and gRPC (an XHTTP of the template's has none of
// the Xray names the model keeps), a WebSocket header but Host, no port.
func ReadProxy(m map[string]any) (model.Proxy, bool) {
	p := model.Proxy{Name: str(m["name"]), Type: str(m["type"]), Server: str(m["server"]), Port: number(m["port"])}
	if p.Port == 0 {
		return p, false
	}
	// A proxy that leaves udp out is taken as carrying it, as the sing-box config made of
	// the profile has always had it.
	p.UDP = m["udp"] != false
	// A template may give the ALPN of any protocol over TLS, the panel of the QUIC ones.
	p.ALPN = strs(m["alpn"])
	switch p.Type {
	case "vless":
		p.UUID, p.Flow = str(m["uuid"]), str(m["flow"])
		if e := str(m["encryption"]); e != "none" {
			p.Encryption = e
		}
	case "trojan", "anytls":
		p.Password = str(m["password"])
	case "hysteria2":
		p.Password, p.Ports, p.UDP = str(m["password"]), str(m["ports"]), true
		if o := str(m["obfs"]); o != "" {
			p.Obfs = &model.Obfs{Type: o, Password: str(m["obfs-password"]),
				MinPacket: number(m["obfs-min-packet-size"]), MaxPacket: number(m["obfs-max-packet-size"])}
		}
	case "tuic":
		// The model's TUIC relays UDP natively, whatever udp-relay-mode the template gives:
		// a TUIC server takes both.
		p.UUID, p.Password, p.UDP = str(m["uuid"]), str(m["password"]), true
		p.Congestion = str(m["congestion-controller"])
	case "ss":
		p.Type, p.Cipher, p.Password = "shadowsocks", str(m["cipher"]), str(m["password"])
		if str(m["plugin"]) != "" || anyMap(m["smux"])["enabled"] == true {
			return p, false
		}
		if m["udp-over-tcp"] == true {
			// mihomo's version is 1 when none is given (or 0), and it refuses one but 1 and 2.
			p.UDPOverTCP = 1
			if s := str(m["udp-over-tcp-version"]); s != "" && s != "0" {
				n, err := strconv.Atoi(s)
				if err != nil || n != 1 && n != 2 {
					return p, false
				}
				p.UDPOverTCP = n
			}
		}
	default:
		return p, false
	}
	reality := anyMap(m["reality-opts"])
	switch p.Type {
	case "vless", "trojan":
		if m["tls"] == true || reality != nil {
			p.TLS = tlsOf(m, reality)
		}
		tr, ok := transportOf(m)
		if !ok {
			return p, false
		}
		p.Transport = tr
	case "hysteria2", "tuic", "anytls": // TLS by themselves
		p.TLS = tlsOf(m, reality)
	}
	return p, true
}

// tlsOf is the TLS of a proxy that has it.
func tlsOf(m, reality map[string]any) *model.TLS {
	t := &model.TLS{SNI: str(m["servername"]), Fingerprint: str(m["client-fingerprint"]), Pin: str(m["fingerprint"]),
		Insecure: m["skip-cert-verify"] == true}
	if t.SNI == "" {
		t.SNI = str(m["sni"])
	}
	if reality != nil {
		t.Reality = &model.Reality{PublicKey: str(reality["public-key"]), ShortID: str(reality["short-id"]),
			MLKEM: reality["support-x25519mlkem768"] == true}
	}
	return t
}

// transportOf is what a VLESS or Trojan proxy carries inside TLS.
func transportOf(m map[string]any) (*model.Transport, bool) {
	switch n := str(m["network"]); n {
	case "", "tcp":
		return &model.Transport{Network: "tcp"}, true
	case "ws":
		w := anyMap(m["ws-opts"])
		tr := &model.Transport{Network: n, Path: str(w["path"])}
		for k, v := range anyMap(w["headers"]) {
			if !strings.EqualFold(k, "Host") {
				return nil, false
			}
			tr.Host = str(v)
		}
		return tr, true
	case "grpc":
		return &model.Transport{Network: n, Service: str(anyMap(m["grpc-opts"])["grpc-service-name"])}, true
	}
	return nil, false
}

func strs(v any) []string {
	var out []string
	for _, x := range anyList(v) {
		out = append(out, str(x))
	}
	return out
}

func anyMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}
