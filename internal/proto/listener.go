package proto

import (
	"encoding/json"
	"errors"
	"fmt"
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
// in microseconds as mihomo reads it (2 h, like Hiddify). Without it a recorded
// ClientHello replays forever: the reply then shows the REALITY certificate instead of
// the target's, and the server is exposed. A template may set its own value.
const RealityMaxTimeDiff = int64(2 * 60 * 60 * 1_000_000)

// Listener renders the mihomo listener: the template minus mikan's section, plus name,
// port, listen address, users and, where the protocol needs it, the node certificate.
func Listener(t Template, name, listen, port string, slots []Slot, cert Cert, o Options) (map[string]any, error) {
	if err := Validate(t, o); err != nil {
		return nil, err
	}
	r := rules[t.Type()]
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
	}
	l["name"], l["port"], l["listen"] = name, port, listen
	if !r.shared {
		l["users"] = users(t.Type(), slots, ext)
	}
	listenerExtra(t, l)
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
func users(typ string, slots []Slot, ext Ext) any {
	switch typ {
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
	case "vmess":
		out := make([]map[string]any, 0, len(slots))
		for _, s := range slots {
			out = append(out, map[string]any{"username": s.Name, "uuid": s.UUID, "alterId": 0})
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

// FromPreset converts the settings of mikan ≤ 0.1.2 (one struct per preset) to a
// template. The panel runs it once per inbound; the node uses it to restore a state
// saved by an older version.
func FromPreset(preset string, settings []byte) (Template, error) {
	var s struct {
		Reality *struct {
			PrivateKey  string   `json:"private_key"`
			ShortIDs    []string `json:"short_ids"`
			Dest        string   `json:"dest"`
			ServerNames []string `json:"server_names"`
		} `json:"reality"`
		Path              string `json:"path"`
		Mode              string `json:"mode"`
		ObfsPassword      string `json:"obfs_password"`
		UpMbps            int    `json:"up_mbps"`
		DownMbps          int    `json:"down_mbps"`
		Masquerade        string `json:"masquerade"`
		CongestionControl string `json:"congestion_control"`
	}
	if err := json.Unmarshal(settings, &s); err != nil {
		return nil, err
	}
	reality := func() map[string]any {
		if s.Reality == nil {
			return nil
		}
		return map[string]any{"dest": s.Reality.Dest, "private-key": s.Reality.PrivateKey, "short-id": anyList(s.Reality.ShortIDs), "server-names": anyList(s.Reality.ServerNames)}
	}
	t := Template{}
	switch preset {
	case "vless_reality_vision":
		t["type"], t["reality-config"] = "vless", reality()
		t[extKey] = map[string]any{"flow": "xtls-rprx-vision"}
	case "vless_reality_xhttp":
		mode := s.Mode
		if mode == "" {
			mode = "stream-one"
		}
		t["type"], t["reality-config"] = "vless", reality()
		t["xhttp-config"] = map[string]any{"path": s.Path, "mode": mode}
	case "hysteria2":
		t["type"], t["alpn"] = "hysteria2", []any{"h3"}
		if s.ObfsPassword != "" {
			t["obfs"], t["obfs-password"] = "salamander", s.ObfsPassword
		}
		if s.UpMbps > 0 && s.DownMbps > 0 {
			t["up"], t["down"] = fmt.Sprintf("%d Mbps", s.UpMbps), fmt.Sprintf("%d Mbps", s.DownMbps)
		}
		if s.Masquerade != "" {
			t["masquerade"] = s.Masquerade
		}
	case "tuic_v5":
		cc := s.CongestionControl
		if cc == "" {
			cc = "bbr"
		}
		t["type"], t["alpn"], t["congestion-controller"] = "tuic", []any{"h3"}, cc
		t["max-idle-time"], t["authentication-timeout"] = 15000, 1000
	default:
		return nil, fmt.Errorf("unknown preset %q", preset)
	}
	return t, nil
}

func anyList(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
