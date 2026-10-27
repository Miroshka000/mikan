// Package mihomo renders subscriptions for the Clash family of apps on the mihomo core
// (and Citrus, mikan's fork of it): their proxies, groups, rules and DNS, the admin's own
// Clash profile, and Stash's flavor of it.
package mihomo

import (
	"mikan/internal/sub/apps"
	"mikan/internal/sub/model"
	"mikan/internal/sub/profile"
)

// defaultReuse makes mihomo reuse a few HTTP/2 connections for XHTTP (Xray clients do this
// by default). Without it every app connection is a fresh TLS handshake, and bursts of
// those get the server's port frozen by the RU DPI.
var defaultReuse = map[string]any{"max-concurrency": "16-32", "h-max-request-times": "600-900", "h-max-reusable-secs": "1800-3000"}

// grpcPingInterval is how often, in seconds, the app pings an idle gRPC connection: a
// middlebox on the way cuts a connection that stays silent, and the app would go on sending
// into it. Stash documents no such option, and its profile goes without (proxy).
const grpcPingInterval = 15

// Proxy is p as a mihomo proxy.
func Proxy(p model.Proxy) map[string]any {
	y := map[string]any{"name": p.Name, "type": typeOf(p), "server": p.Server, "port": p.Port}
	if p.TLS != nil {
		tls(y, p)
	}
	if p.Transport != nil {
		transport(y, p.Transport)
	}
	switch p.Type {
	case "vless":
		y["uuid"] = p.UUID
		if p.Encryption != "" {
			y["encryption"] = p.Encryption
		}
		if p.Flow != "" {
			y["flow"] = p.Flow
		}
	case "trojan", "anytls":
		y["password"] = p.Password
	case "hysteria2":
		y["password"] = p.Password
		if len(p.ALPN) > 0 {
			y["alpn"] = p.ALPN
		}
		if p.Ports != "" {
			y["ports"] = p.Ports
		}
		if o := p.Obfs; o != nil {
			y["obfs"], y["obfs-password"] = o.Type, o.Password
			if o.MinPacket > 0 {
				y["obfs-min-packet-size"] = o.MinPacket
			}
			if o.MaxPacket > 0 {
				y["obfs-max-packet-size"] = o.MaxPacket
			}
		}
	case "tuic":
		// TUIC relays UDP packet by packet ("native"), not over a stream.
		y["uuid"], y["password"], y["alpn"] = p.UUID, p.Password, p.ALPN
		y["congestion-controller"], y["udp-relay-mode"] = p.Congestion, "native"
	case "trusttunnel":
		y["username"], y["password"] = p.Username, p.Password
	case "shadowquic":
		y["username"], y["password"], y["alpn"] = p.Username, p.Password, p.ALPN
	case "mieru":
		// The panel's Mieru runs over TCP only.
		y["transport"], y["username"], y["password"] = "TCP", p.Username, p.Password
	case "shadowsocks":
		y["cipher"], y["password"] = p.Cipher, p.Password
		if p.UDPOverTCP > 0 {
			y["udp-over-tcp"], y["udp-over-tcp-version"] = true, p.UDPOverTCP
		}
	case "sudoku":
		s := p.Sudoku
		y["key"], y["aead-method"] = s.Key, s.AEAD
		if s.PaddingMin != nil {
			y["padding-min"] = *s.PaddingMin
		}
		if s.PaddingMax != nil {
			y["padding-max"] = *s.PaddingMax
		}
		if s.TableType != "" {
			y["table-type"] = s.TableType
		}
		if s.PureDownlink != nil {
			y["enable-pure-downlink"] = *s.PureDownlink
		}
		if s.HTTPMask != nil {
			y["httpmask"] = s.HTTPMask
		}
	case "snell":
		y["psk"], y["version"] = p.Snell.PSK, p.Snell.Version
		if p.Snell.ObfsOpts != nil {
			y["obfs-opts"] = p.Snell.ObfsOpts
		}
	}
	// Hysteria2 and TUIC carry UDP by themselves: mihomo takes no switch for them.
	if p.Type != "hysteria2" && p.Type != "tuic" {
		y["udp"] = p.UDP
	}
	return y
}

// typeOf is mihomo's name of p's protocol.
func typeOf(p model.Proxy) string {
	if p.Type == "shadowsocks" {
		return "ss"
	}
	return p.Type
}

// sniKey: vless calls the TLS name "servername", everything else "sni".
func sniKey(typ string) string {
	if typ == "vless" {
		return "servername"
	}
	return "sni"
}

func tls(y map[string]any, p model.Proxy) {
	t := p.TLS
	switch p.Type {
	case "vless", "trojan", "anytls": // the others are TLS by themselves
		y["tls"] = true
	}
	if t.Fingerprint != "" {
		y["client-fingerprint"] = t.Fingerprint
	}
	if t.SNI != "" {
		y[sniKey(p.Type)] = t.SNI
	}
	if r := t.Reality; r != nil {
		opts := map[string]any{"public-key": r.PublicKey, "short-id": r.ShortID}
		if r.MLKEM {
			opts["support-x25519mlkem768"] = true
		}
		y["reality-opts"] = opts
	}
	if t.Pin != "" {
		// A self-signed certificate is pinned, and only it is taken.
		y["fingerprint"], y["skip-cert-verify"] = t.Pin, false
	}
}

func transport(y map[string]any, tr *model.Transport) {
	switch tr.Network {
	case "xhttp":
		opts := map[string]any{"reuse-settings": defaultReuse}
		for k, v := range map[string]string{"path": tr.Path, "mode": tr.XHTTP.Mode, "host": tr.Host} {
			if v != "" {
				opts[k] = v
			}
		}
		for _, s := range tr.XHTTP.Settings {
			opts[s.Mihomo] = s.Value
		}
		if tr.XHTTP.Reuse != nil {
			reuse := map[string]any{}
			for _, s := range tr.XHTTP.Reuse {
				reuse[s.Mihomo] = s.Value
			}
			opts["reuse-settings"] = reuse
		}
		y["network"], y["xhttp-opts"] = "xhttp", opts
	case "grpc":
		y["network"], y["grpc-opts"] = "grpc", map[string]any{"grpc-service-name": tr.Service, "ping-interval": grpcPingInterval}
	case "ws":
		opts := map[string]any{"path": tr.Path}
		if tr.Host != "" {
			opts["headers"] = map[string]any{"Host": tr.Host}
		}
		y["network"], y["ws-opts"] = "ws", opts
	default:
		y["network"] = "tcp"
	}
}

// proxy is x as the app of p takes it: Proxy, without the gRPC ping interval for Stash.
func proxy(p profile.Profile, x model.Proxy) map[string]any {
	y := Proxy(x)
	if o, ok := y["grpc-opts"].(map[string]any); ok && p.App.Family == apps.FamilyStash {
		delete(o, "ping-interval")
	}
	return y
}
