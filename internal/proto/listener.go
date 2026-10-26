package proto

import (
	"encoding/json"
	"errors"
	"net"
)

// Slot is one pre-provisioned credential set: every listener carries all of them, so a
// new user needs no listener restart.
type Slot struct {
	Name   string `json:"name"`
	UUID   string `json:"uuid"`
	Secret string `json:"secret"`
}

// Cert is the node's certificate on disk.
type Cert struct {
	CertPath, KeyPath string
}

// RealityMaxTimeDiff bounds how far a REALITY client's clock may be from the server's,
// in milliseconds as the Citrus core reads it, like Xray's maxTimeDiff (2 h, like
// Hiddify; mihomo took microseconds). Without it a recorded ClientHello replays forever:
// the reply then shows the REALITY certificate instead of the target's, and the server is
// exposed. A template may set its own value.
const RealityMaxTimeDiff = int64(2 * 60 * 60 * 1_000)

// Listener renders the mihomo listener: the template minus mikan's section, plus name,
// port, listen address, users and, where the protocol needs it, the node certificate.
func Listener(t Template, name, listen, port string, slots []Slot, cert Cert, o Options) (map[string]any, error) {
	if err := Validate(t, o); err != nil {
		return nil, err
	}
	ext := t.Ext()
	l := make(map[string]any, len(t)+5)
	for k, v := range t {
		if k != extKey {
			l[k] = clone(v)
		}
	}
	if listen == "" {
		listen = "0.0.0.0"
	}
	if rc, ok := l["reality-config"].(map[string]any); ok {
		if _, set := rc["max-time-difference"]; !set {
			rc["max-time-difference"] = RealityMaxTimeDiff
		}
		// A dest given by name is dialed at the address it resolves to there and then, which
		// must be public: the panel checked the name when it was saved, and a name that leads
		// inside later does not reach in. The panel's own HTTPS (self-steal) is the node's own
		// loopback, which "localhost" names.
		if host, _, err := net.SplitHostPort(str(rc["dest"])); err == nil && host != "localhost" {
			rc["public-dest"] = true
		}
	}
	l["name"], l["port"], l["listen"] = name, port, listen
	if !Shared(t) {
		l["users"] = users(t, slots, ext)
	}
	listenerExtra(t, l)
	cdnListener(t, l)
	if t.NodeCert() {
		if cert.CertPath == "" {
			return nil, errors.New("no node certificate for " + t.Type())
		}
		l["certificate"], l["private-key"] = cert.CertPath, cert.KeyPath
	}
	return l, nil
}

// users follows mihomo's per-type layout. The name the listener reports in
// metadata.InUser is the slot name, except TUIC which reports the UUID; the node's
// registry resolves both.
func users(t Template, slots []Slot, ext Ext) any {
	switch t.Type() {
	case "vless":
		out := make([]map[string]any, 0, len(slots))
		for _, s := range slots {
			u := map[string]any{"username": s.Name, "uuid": s.UUID}
			if ext.Flow != "" {
				u["flow"] = ext.Flow
			}
			out = append(out, u)
		}
		return out
	case "shadowsocks":
		// The listener's password stays the server's key; each user has one of its own.
		cipher := t.str("cipher")
		out := make([]map[string]any, 0, len(slots))
		for _, s := range slots {
			out = append(out, map[string]any{"name": s.Name, "password": SSUserKey(cipher, s.Secret)})
		}
		return out
	case "trojan", "trusttunnel", "shadowquic":
		out := make([]map[string]any, 0, len(slots))
		for _, s := range slots {
			out = append(out, map[string]any{"username": s.Name, "password": s.Secret})
		}
		return out
	case "tuic":
		m := make(map[string]string, len(slots))
		for _, s := range slots {
			m[s.UUID] = s.Secret
		}
		return m
	default: // hysteria2, anytls, mieru
		m := make(map[string]string, len(slots))
		for _, s := range slots {
			m[s.Name] = s.Secret
		}
		return m
	}
}

func clone(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if json.Unmarshal(b, &out) != nil {
		return v
	}
	return out
}
