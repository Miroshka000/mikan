// Package xray renders subscriptions for the apps on the Xray core (Happ, INCY, v2RayTun,
// v2rayNG…) and for every app that takes share links: the links themselves, and the
// routing Happ and INCY take beside them.
package xray

import (
	"encoding/json"
	"net"
	"net/url"
	"strconv"

	"mikan/internal/sub/model"
)

// Link is p's share link, "" for the protocols that have no common link format (the
// mihomo-only ones): only Clash profiles carry those.
func Link(p model.Proxy) string {
	q := url.Values{}
	if p.TLS != nil {
		security(q, p)
	}
	if p.Transport != nil {
		transport(q, p.Transport)
	}
	addr := net.JoinHostPort(p.Server, strconv.Itoa(p.Port))
	name := url.PathEscape(p.Name)
	switch p.Type {
	case "vless":
		q.Set("encryption", "none")
		if p.Encryption != "" {
			q.Set("encryption", p.Encryption)
		}
		if p.Flow != "" {
			q.Set("flow", p.Flow)
		}
		return "vless://" + p.UUID + "@" + addr + "?" + q.Encode() + "#" + name
	case "trojan":
		return "trojan://" + url.PathEscape(p.Password) + "@" + addr + "?" + q.Encode() + "#" + name
	case "hysteria2":
		if p.Ports != "" {
			q.Set("mport", p.Ports)
		}
		if o := p.Obfs; o != nil {
			q.Set("obfs", o.Type)
			q.Set("obfs-password", o.Password)
		}
		return "hysteria2://" + url.PathEscape(p.Password) + "@" + addr + "/?" + q.Encode() + "#" + name
	case "tuic":
		// TUIC relays UDP packet by packet ("native"), not over a stream.
		q.Set("congestion_control", p.Congestion)
		q.Set("alpn", p.ALPN[0])
		q.Set("udp_relay_mode", "native")
		return "tuic://" + p.UUID + ":" + url.PathEscape(p.Password) + "@" + addr + "?" + q.Encode() + "#" + name
	case "anytls":
		// The scheme has no TLS settings of its own: the name and the pin only.
		q.Del("security")
		q.Del("fp")
		return "anytls://" + url.PathEscape(p.Password) + "@" + addr + "/?" + q.Encode() + "#" + name
	case "shadowsocks":
		// SIP002 for 2022 methods: "method:key" percent-encoded, not base64.
		return "ss://" + url.UserPassword(p.Cipher, p.Password).String() + "@" + addr + "#" + name
	}
	return ""
}

func security(q url.Values, p model.Proxy) {
	t := p.TLS
	if r := t.Reality; r != nil {
		for k, v := range map[string]string{"security": "reality", "sni": t.SNI, "fp": t.Fingerprint, "pbk": r.PublicKey, "sid": r.ShortID} {
			q.Set(k, v)
		}
		return
	}
	switch p.Type {
	case "vless", "trojan", "anytls":
		q.Set("security", "tls")
		q.Set("fp", t.Fingerprint)
	}
	if t.SNI != "" {
		q.Set("sni", t.SNI)
	}
	if t.Pin == "" {
		return
	}
	// A self-signed certificate is pinned where the app reads one: Hysteria2's pinSHA256,
	// Xray's pcs (pinnedPeerCertSha256), next to the insecure flag they need to take a
	// certificate without a CA at all.
	switch p.Type {
	case "hysteria2":
		q.Set("insecure", "1")
		q.Set("pinSHA256", t.Pin)
	case "tuic":
		q.Set("allow_insecure", "1") // the TUIC link scheme has no pin
	default:
		q.Set("allowInsecure", "1")
		q.Set("insecure", "1")
		q.Set("pcs", t.Pin)
	}
}

func transport(q url.Values, tr *model.Transport) {
	switch tr.Network {
	case "xhttp":
		for k, v := range map[string]string{"path": tr.Path, "mode": tr.XHTTP.Mode, "host": tr.Host} {
			if v != "" {
				q.Set(k, v)
			}
		}
		// Padding, session and uplink settings must match on both ends: they go in the
		// link's "extra", under Xray's names.
		extra := map[string]any{}
		for _, s := range tr.XHTTP.Settings {
			extra[s.Xray] = s.Value
		}
		if tr.XHTTP.Reuse != nil {
			xmux := map[string]any{}
			for _, s := range tr.XHTTP.Reuse {
				xmux[s.Xray] = s.Value
			}
			extra["xmux"] = xmux
		}
		if len(extra) > 0 {
			b, _ := json.Marshal(extra)
			q.Set("extra", string(b))
		}
		q.Set("type", "xhttp")
	case "grpc":
		q.Set("type", "grpc")
		q.Set("serviceName", tr.Service)
		q.Set("mode", "gun")
	case "ws":
		q.Set("type", "ws")
		q.Set("path", tr.Path)
		if tr.Host != "" {
			q.Set("host", tr.Host)
		}
	default:
		q.Set("type", "tcp")
	}
}
