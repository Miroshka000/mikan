package xray

import (
	"encoding/hex"
	"strings"

	"mikan/internal/proto"
	"mikan/internal/sub/model"
)

// outbound is p as an outbound of an xray JSON config, tagged tag; false for what xray
// cannot carry: TUIC, AnyTLS and the mihomo-only protocols, Hysteria2 with Gecko, and a
// certificate pinned by anything but its SHA-256, for which xray refuses the whole config.
// No outbound multiplexes (mux): it breaks XTLS Vision, and gRPC and XHTTP multiplex on
// their own.
func outbound(p model.Proxy, tag string) (map[string]any, bool) {
	o := map[string]any{"tag": tag}
	switch p.Type {
	case "vless":
		user := map[string]any{"id": p.UUID, "encryption": "none"}
		if p.Encryption != "" {
			user["encryption"] = p.Encryption
		}
		if p.Flow != "" {
			user["flow"] = p.Flow
		}
		o["protocol"] = "vless"
		o["settings"] = map[string]any{"vnext": []map[string]any{{"address": p.Server, "port": p.Port, "users": []map[string]any{user}}}}
	case "trojan":
		o["protocol"] = "trojan"
		o["settings"] = map[string]any{"servers": []map[string]any{{"address": p.Server, "port": p.Port, "password": p.Password}}}
	case "shadowsocks":
		server := map[string]any{"address": p.Server, "port": p.Port, "method": p.Cipher, "password": p.Password}
		if p.UDPOverTCP > 0 {
			// The node's listener has no UDP port: UDP goes inside the TCP connection.
			server["uot"], server["UoTVersion"] = true, p.UDPOverTCP
		}
		o["protocol"] = "shadowsocks"
		o["settings"] = map[string]any{"servers": []map[string]any{server}}
		return o, true
	case "hysteria2":
		return hysteria(p, o)
	default:
		return nil, false
	}
	ss, ok := stream(p)
	if !ok {
		return nil, false
	}
	o["streamSettings"] = ss
	return o, true
}

// The keepalive of a gRPC connection in an xray JSON profile (grpcSettings), in seconds: an
// idle connection is pinged after grpcIdleTimeout and given up when no answer comes within
// grpcHealthCheckTimeout, so that the app does not go on sending into one a middlebox cut
// while it was silent. Share links have no place for them.
const (
	grpcIdleTimeout        = 15
	grpcHealthCheckTimeout = 10
)

// stream is the transport and the security of a VLESS or Trojan outbound.
func stream(p model.Proxy) (map[string]any, bool) {
	tr := p.Transport
	if tr == nil {
		tr = &model.Transport{Network: "tcp"}
	}
	ss := map[string]any{"network": tr.Network}
	switch tr.Network {
	case "", "tcp":
		ss["network"] = "tcp"
	case "ws":
		ws := map[string]any{"path": tr.Path}
		if tr.Host != "" {
			ws["host"] = tr.Host
		}
		ss["wsSettings"] = ws
	case "grpc":
		ss["grpcSettings"] = map[string]any{"serviceName": tr.Service, "idle_timeout": grpcIdleTimeout, "health_check_timeout": grpcHealthCheckTimeout}
	case "xhttp":
		x := map[string]any{}
		for k, v := range map[string]string{"path": tr.Path, "host": tr.Host} {
			if v != "" {
				x[k] = v
			}
		}
		if tr.XHTTP != nil && tr.XHTTP.Mode != "" {
			x["mode"] = tr.XHTTP.Mode
		}
		// The settings both ends must agree on go in "extra", as in the share link: xray
		// takes everything but the path, the host and the mode from it.
		if extra := xhttpExtra(tr.XHTTP); len(extra) > 0 {
			x["extra"] = extra
		}
		ss["xhttpSettings"] = x
	default:
		return nil, false
	}
	return ss, secure(ss, p)
}

// secure puts p's TLS in the stream settings ss: REALITY, or TLS on the node's certificate,
// pinned by its SHA-256 when it is self-signed; false for a pin xray cannot read.
func secure(ss map[string]any, p model.Proxy) bool {
	t := p.TLS
	if t == nil {
		ss["security"] = "none"
		return true
	}
	if r := t.Reality; r != nil {
		fp := t.Fingerprint
		if fp == "" {
			fp = proto.DefaultFingerprint // REALITY dials with uTLS or not at all
		}
		ss["security"] = "reality"
		ss["realitySettings"] = map[string]any{"serverName": t.SNI, "fingerprint": fp, "publicKey": r.PublicKey, "shortId": r.ShortID}
		return true
	}
	tls := map[string]any{}
	if t.SNI != "" {
		tls["serverName"] = t.SNI
	}
	if t.Fingerprint != "" {
		tls["fingerprint"] = t.Fingerprint
	}
	if p.Type == "hysteria2" && len(p.ALPN) > 0 {
		tls["alpn"] = p.ALPN
	}
	if t.Pin != "" {
		// xray takes a certificate without a CA by its pin alone: allowInsecure, which the
		// share links still carry for older apps, is refused by xray now.
		if !pinned(t.Pin) {
			return false
		}
		tls["pinnedPeerCertSha256"] = t.Pin
	}
	ss["security"] = "tls"
	ss["tlsSettings"] = tls
	return true
}

// pinned says whether pin is a certificate's SHA-256 in hex, the one form xray pins by.
func pinned(pin string) bool {
	b, err := hex.DecodeString(strings.ReplaceAll(pin, ":", ""))
	return err == nil && len(b) == 32
}

// hysteria is a Hysteria2 outbound, xray's hysteria of version 2: the password and the
// node's certificate, with the obfuscation as a mask of its packets. The port hopping stays
// out: its mask (udpHop) is in Xray from v26.9.9 only, the apps do not say their core's
// version, and an older core refuses the whole config; the node's own port takes the
// client all the same. Gecko is mihomo's alone.
func hysteria(p model.Proxy, o map[string]any) (map[string]any, bool) {
	ss := map[string]any{"network": "hysteria", "hysteriaSettings": map[string]any{"version": 2, "auth": p.Password}}
	if p.TLS == nil {
		p.TLS = &model.TLS{}
	}
	if !secure(ss, p) {
		return nil, false
	}
	var masks []map[string]any
	if ob := p.Obfs; ob != nil {
		if ob.Type != "salamander" {
			return nil, false
		}
		masks = append(masks, map[string]any{"type": "salamander", "settings": map[string]any{"password": ob.Password}})
	}
	if len(masks) > 0 {
		ss["finalmask"] = map[string]any{"udp": masks}
	}
	o["protocol"] = "hysteria"
	o["settings"] = map[string]any{"version": 2, "address": p.Server, "port": p.Port}
	o["streamSettings"] = ss
	return o, true
}
