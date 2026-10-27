// Package build makes the proxies of a subscription: every inbound the user may use, on
// every node, with the user's keys, as the model.Proxy each core renders.
package build

import (
	"net"
	"strconv"

	"mikan/internal/proto"
	"mikan/internal/sub/model"
)

// Input is what a subscription knows of one user and one node for an inbound of it.
type Input struct {
	Name     string // the proxy's name in the subscription
	Host     string // where the apps connect
	Port     int    // the first port of PortSpec
	PortSpec string // "443", or a range for Hysteria2's port hopping
	// SNI is the TLS name of the node's certificate for the protocols on it; "" on an
	// install without a domain.
	SNI string
	// PinSHA256 is the hex SHA-256 of the node's certificate when it is self-signed.
	PinSHA256 string
	// Fingerprint is the panel's default uTLS profile for templates that set none; ""
	// or one of the wrong shape: proto.DefaultFingerprint.
	Fingerprint string
	Keys        proto.Slot
	// SSOneKey: the node runs Shadowsocks-2022 with one key for everyone (older than the
	// key per user): the app gets the server's key alone, as that node expects.
	SSOneKey bool
	// Client is what the apps get in place of the template's mikan.client: a field left
	// empty keeps the template's.
	Client proto.ClientOverrides
}

// Proxy is the client side of the template t for in: what the apps of the user dial.
// The proxy may share maps with t (Sudoku's httpmask, Snell's obfs-opts), so the caller
// passes a template of its own, as parseTemplate and proto.Parse give.
func Proxy(t proto.Template, in Input) (model.Proxy, error) {
	if err := proto.Validate(t, proto.Options{AnyDest: true}); err != nil {
		return model.Proxy{}, err
	}
	ext := t.Ext()
	// From here on in.Client is the one the apps get; nothing else reads mikan.client.
	in.Client = overrides(in.Client, ext.Client)
	p := model.Proxy{Name: in.Name, Type: t.Type(), Server: in.Host, Port: in.Port}
	if in.Client.Server != "" {
		p.Server = in.Client.Server
	}
	if in.Client.Port > 0 {
		p.Port = in.Client.Port
	}
	if err := security(&p, t, ext, in); err != nil {
		return model.Proxy{}, err
	}
	transport(&p, t, in)
	if err := keys(&p, t, ext, in); err != nil {
		return model.Proxy{}, err
	}
	return p, nil
}

// overrides is c with the empty fields taken from the template's own; a fingerprint of the
// wrong shape counts as empty (fingerprint still guards the template's).
func overrides(c, tpl proto.ClientOverrides) proto.ClientOverrides {
	if c.Server == "" {
		c.Server = tpl.Server
	}
	if c.Port <= 0 {
		c.Port = tpl.Port
	}
	if c.SNI == "" {
		c.SNI = tpl.SNI
	}
	if c.Host == "" {
		c.Host = tpl.Host
	}
	if !proto.ValidFingerprint(c.Fingerprint) {
		c.Fingerprint = tpl.Fingerprint
	}
	return c
}

// fingerprint: the inbound's own choice, then the panel's default. A malformed value saved
// before the panel checked them falls through rather than reaching the apps.
func fingerprint(in Input) string {
	for _, fp := range []string{in.Client.Fingerprint, in.Fingerprint} {
		if proto.ValidFingerprint(fp) {
			return fp
		}
	}
	return proto.DefaultFingerprint
}

// security is the TLS the apps send: REALITY, or the node's certificate for the protocols
// on it, pinned when it is self-signed.
func security(p *model.Proxy, t proto.Template, ext proto.Ext, in Input) error {
	if r, ok := t["reality-config"].(map[string]any); ok {
		pbk, err := proto.RealityPublicKey(str(r["private-key"]))
		if err != nil {
			return err
		}
		sni := in.Client.SNI
		if sni == "" {
			sni = strs(r["server-names"])[0]
		}
		// A server that requires the post-quantum key exchange lets in only clients that
		// offer it, and mihomo does only when told.
		p.TLS = &model.TLS{SNI: sni, Fingerprint: fingerprint(in),
			Reality: &model.Reality{PublicKey: pbk, ShortID: strs(r["short-id"])[0], MLKEM: r["require-x25519mlkem768"] == true}}
		return nil
	}
	if !t.NodeCert() {
		return nil
	}
	tls := &model.TLS{SNI: in.Client.SNI}
	if tls.SNI == "" {
		tls.SNI = in.SNI
	}
	if proto.UsesFingerprint(t) { // QUIC protocols have no uTLS
		tls.Fingerprint = fingerprint(in)
	}
	// Through a CDN the apps see its certificate, never the node's.
	if in.PinSHA256 != "" && ext.CDN == nil {
		tls.Pin = in.PinSHA256
	}
	p.TLS = tls
	return nil
}

// transport is what VLESS and Trojan carry inside TLS. UDP goes over every one of them,
// XHTTP too, to the Citrus server.
func transport(p *model.Proxy, t proto.Template, in Input) {
	if p.Type != "vless" && p.Type != "trojan" {
		return
	}
	tr := &model.Transport{Network: proto.Transport(t)}
	switch tr.Network {
	case "xhttp":
		x, _ := t["xhttp-config"].(map[string]any)
		tr.Path, tr.Host = str(x["path"]), str(x["host"])
		if h := in.Client.Host; h != "" {
			tr.Host = h
		}
		settings, reuse := proto.XHTTPClient(t)
		tr.XHTTP = &model.XHTTP{Mode: str(x["mode"]), Settings: settingsOf(settings), Reuse: settingsOf(reuse)}
	case "grpc":
		tr.Service = str(t["grpc-service-name"])
	case "ws":
		tr.Path, tr.Host = str(t["ws-path"]), in.Client.Host
	}
	p.Transport, p.UDP = tr, true
}

// settingsOf keeps nil apart from empty: an empty reuse of the inbound's own still
// replaces a core's default.
func settingsOf(in []proto.XHTTPSetting) []model.Setting {
	if in == nil {
		return nil
	}
	out := make([]model.Setting, 0, len(in))
	for _, s := range in {
		out = append(out, model.Setting{Mihomo: s.Mihomo, Xray: s.Xray, Value: s.Value})
	}
	return out
}

// keys puts in the user's keys as the protocol takes them, and its own settings.
func keys(p *model.Proxy, t proto.Template, ext proto.Ext, in Input) error {
	s := in.Keys
	switch p.Type {
	case "vless":
		p.UUID, p.Flow = s.UUID, ext.Flow
		if proto.NeedsOf(t).Encryption {
			enc, err := proto.ClientEncryption(str(t["decryption"]))
			if err != nil {
				return err
			}
			p.Encryption = enc
		}
	case "trojan":
		p.Password = s.Secret
	case "hysteria2":
		p.Password, p.ALPN, p.UDP = s.Secret, strs(t["alpn"]), true
		// A port of the inbound's own (a proxy in front) replaces the node's hopping range.
		if in.PortSpec != "" && in.PortSpec != strconv.Itoa(in.Port) && p.Port == in.Port {
			p.Ports = in.PortSpec
		}
		if obfs := str(t["obfs"]); obfs != "" {
			o := &model.Obfs{Type: obfs, Password: str(t["obfs-password"])}
			// Gecko's sizes are the sender's own; the client gets the server's.
			o.MinPacket, _ = toInt(t["obfs-min-packet-size"])
			o.MaxPacket, _ = toInt(t["obfs-max-packet-size"])
			p.Obfs = o
		}
	case "tuic":
		p.Congestion = str(t["congestion-controller"])
		if p.Congestion == "" {
			p.Congestion = "bbr"
		}
		p.UUID, p.Password, p.ALPN, p.UDP = s.UUID, s.Secret, orH3(strs(t["alpn"])), true
	case "anytls":
		p.Password, p.UDP = s.Secret, true
	case "trusttunnel":
		p.Username, p.Password, p.UDP = s.Name, s.Secret, true
	case "shadowquic":
		up, _ := t["jls-upstream"].(map[string]any)
		sni := in.Client.SNI
		if sni == "" {
			sni = str(up["sni"])
		}
		if sni == "" {
			sni, _, _ = net.SplitHostPort(str(up["addr"]))
		}
		p.Username, p.Password, p.TLS, p.ALPN, p.UDP = s.Name, s.Secret, &model.TLS{SNI: sni}, orH3(strs(t["alpn"])), true
	case "mieru":
		// The Citrus server knows who sends UDP over Mieru (mihomo's did not).
		p.Username, p.Password, p.UDP = s.Name, s.Secret, true
	case "shadowsocks":
		cipher, key := str(t["cipher"]), str(t["password"])
		if !proto.Shared(t) && !in.SSOneKey {
			key += ":" + proto.SSUserKey(cipher, s.Secret)
		}
		// UDP goes inside the TCP stream (UDP over TCP, v2): the listener has no UDP port.
		p.Cipher, p.Password, p.UDP, p.UDPOverTCP = cipher, key, true, 2
	case "sudoku":
		sd := &model.Sudoku{Key: str(t["key"]), AEAD: str(t["aead-method"]), TableType: str(t["table-type"])}
		if sd.AEAD == "" {
			sd.AEAD = "chacha20-poly1305"
		}
		if n, ok := toInt(t["padding-min"]); ok {
			sd.PaddingMin = &n
		}
		if n, ok := toInt(t["padding-max"]); ok {
			sd.PaddingMax = &n
		}
		// The downlink mode must match the node's, or the client cannot read its replies.
		if b, ok := t["enable-pure-downlink"].(bool); ok {
			sd.PureDownlink = &b
		}
		sd.HTTPMask, _ = t["httpmask"].(map[string]any)
		p.Sudoku, p.UDP = sd, true
	case "snell":
		// Snell carries UDP from v3 on.
		v := proto.SnellVersion(t)
		obfs, _ := t["obfs-opts"].(map[string]any)
		p.Snell, p.UDP = &model.Snell{PSK: str(t["psk"]), Version: v, ObfsOpts: obfs}, v >= 3
	default:
		return &proto.Error{Code: "config_type", Field: "type"}
	}
	return nil
}

func orH3(alpn []string) []string {
	if len(alpn) == 0 {
		return []string{"h3"}
	}
	return alpn
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// strs reads a YAML list of strings or a single one.
func strs(v any) []string {
	switch x := v.(type) {
	case string:
		if x == "" {
			return nil
		}
		return []string{x}
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return x
	}
	return nil
}

// toInt reads a YAML number: an int, or a float64 that is a whole one.
func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		if n == float64(int(n)) {
			return int(n), true
		}
	}
	return 0, false
}
