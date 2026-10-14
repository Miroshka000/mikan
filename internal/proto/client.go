package proto

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strconv"

	"mikan/internal/hostname"
)

// ClientInput is what a subscription knows about one user and the node.
type ClientInput struct {
	Name      string // proxy name in the subscription
	Host      string // address clients connect to
	Port      int    // first port of PortSpec
	PortSpec  string // "443" or a range for Hysteria2 port hopping
	SNI       string // TLS name for node-certificate protocols; empty on IP-only installs
	PinSHA256 string // hex SHA-256 of the node certificate when it is self-signed
	// Fingerprint is the panel's default uTLS profile for templates that set none; empty
	// or unknown means DefaultFingerprint.
	Fingerprint string
	Slot        Slot
}

// Fingerprints are the uTLS profiles that mihomo (component/tls/utls.go, v1.19.31), Xray
// and sing-box all accept, so a share link and a Clash profile mimic the same browser.
// mihomo also knows chrome120, firefox120, safari16 and deprecated ones Xray refuses.
var Fingerprints = []string{"chrome", "firefox", "safari", "ios", "android", "edge", "360", "qq", "random", "randomized"}

// DefaultFingerprint is what clients get when neither the inbound nor the panel picks one.
const DefaultFingerprint = "chrome"

// KnownFingerprint says whether s is one of Fingerprints.
func KnownFingerprint(s string) bool { return slices.Contains(Fingerprints, s) }

// fingerprintPattern is the shape of an own fingerprint the admin types: a uTLS profile
// name of mihomo or Xray that the list leaves out (chrome120, randomizednoalpn, …).
// Nothing else fits, so the value cannot break a link or a profile.
var fingerprintPattern = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)

// ValidFingerprint says whether s may be used: one of Fingerprints or an own one of the
// right shape. An own one is the admin's call: an app that does not know it may refuse
// the link (Xray) or connect without uTLS (mihomo).
func ValidFingerprint(s string) bool { return KnownFingerprint(s) || fingerprintPattern.MatchString(s) }

// UsesFingerprint says whether clients of t dial through uTLS, so a fingerprint applies.
func UsesFingerprint(t Template) bool {
	switch t.Type() {
	case "vless", "vmess", "trojan": // REALITY or the node certificate, never plain
		return t.section("reality-config") != nil || t.Ext().TLS == "node"
	case "anytls", "trusttunnel":
		return true
	}
	return false
}

// SetFingerprint writes the inbound's own fingerprint into mikan.client; "" removes it,
// so the panel's default applies again.
func SetFingerprint(t Template, fp string) error {
	if fp != "" && !ValidFingerprint(fp) {
		return fail("config_fingerprint", extKey+".client.fingerprint")
	}
	setClient(t, "fingerprint", fp)
	return nil
}

// ClientSNI says whether clients of t send a TLS name the admin may change: the node
// certificate's protocols. A REALITY client sends the target's name, which the node
// checks against server-names, so another name would not get through.
func ClientSNI(t Template) bool {
	return t.section("reality-config") == nil && (rules[t.Type()].cert || t.Ext().TLS == "node")
}

// SetClientEndpoint writes where clients connect when it is not the node itself, e.g. a
// TCP proxy (nginx stream, HAProxy) in front of an inbound on 127.0.0.1. An empty server
// or SNI, or port 0, removes that override: the node's address, the inbound's port and
// the usual TLS name apply again.
func SetClientEndpoint(t Template, server string, port int, sni string) error {
	switch {
	case server != "" && !hostname.Valid(server):
		return fail("config_client_server", extKey+".client.server")
	case port < 0 || port > 65535:
		return fail("config_client_port", extKey+".client.port")
	case sni != "" && (!hostname.Name(sni) || !ClientSNI(t)):
		return fail("config_client_sni", extKey+".client.sni")
	}
	setClient(t, "server", server)
	setClient(t, "port", port)
	setClient(t, "sni", sni)
	return nil
}

// setClient writes one key of mikan.client; an empty value removes it, and with it the
// sections it leaves empty.
func setClient(t Template, key string, v any) {
	unset := v == "" || v == 0
	ext, _ := t[extKey].(map[string]any)
	if ext == nil {
		if unset {
			return
		}
		ext = map[string]any{}
		t[extKey] = ext
	}
	client, _ := ext["client"].(map[string]any)
	if client == nil {
		client = map[string]any{}
	}
	if unset {
		delete(client, key)
	} else {
		client[key] = v
	}
	switch {
	case len(client) > 0:
		ext["client"] = client
	default:
		delete(ext, "client")
	}
	if len(ext) == 0 {
		delete(t, extKey)
	}
}

// Client is one proxy in both subscription formats.
type Client struct {
	Mihomo map[string]any // mihomo proxy
	URI    string         // share link for Xray/sing-box apps
}

// xmux makes mihomo reuse a few HTTP/2 connections for XHTTP (Xray clients do this by
// default). Without it every app connection is a fresh TLS handshake, and bursts of
// those get the server's port frozen by the RU DPI.
var xmux = map[string]any{"max-concurrency": "16-32", "h-max-request-times": "600-900", "h-max-reusable-secs": "1800-3000"}

// xhttpShared are the xhttp-config keys both ends read (mihomo v1.19.31 XHTTPConfig and
// XHTTPOptions), with their names in the "extra" of an Xray share link.
var xhttpShared = []struct{ mihomo, xray string }{
	{"x-padding-bytes", "xPaddingBytes"}, {"x-padding-obfs-mode", "xPaddingObfsMode"},
	{"x-padding-key", "xPaddingKey"}, {"x-padding-header", "xPaddingHeader"},
	{"x-padding-placement", "xPaddingPlacement"}, {"x-padding-method", "xPaddingMethod"},
	{"uplink-http-method", "uplinkHTTPMethod"},
	{"session-placement", "sessionIDPlacement"}, {"session-key", "sessionIDKey"}, // Xray's own names; mihomo reads both
	{"seq-placement", "seqPlacement"}, {"seq-key", "seqKey"},
	{"uplink-data-placement", "uplinkDataPlacement"}, {"uplink-data-key", "uplinkDataKey"},
	{"uplink-chunk-size", "uplinkChunkSize"}, {"sc-max-each-post-bytes", "scMaxEachPostBytes"},
}

// ClientConfig derives the client side of a template.
func ClientConfig(t Template, in ClientInput) (Client, error) {
	if err := Validate(t, Options{AnyDest: true}); err != nil {
		return Client{}, err
	}
	ext := t.Ext()
	host, port := in.Host, in.Port
	if ext.Client.Server != "" {
		host = ext.Client.Server
	}
	if ext.Client.Port > 0 {
		port = ext.Client.Port
	}
	c := clientBuilder{t: t, ext: ext, in: in, host: host, port: port,
		y: map[string]any{"name": in.Name, "type": t.Type(), "server": host, "port": port}, q: url.Values{}}
	if err := c.security(); err != nil {
		return Client{}, err
	}
	c.transport()
	return c.finish()
}

type clientBuilder struct {
	t    Template
	ext  Ext
	in   ClientInput
	host string
	port int
	y    map[string]any
	q    url.Values
}

func (c *clientBuilder) addr() string { return net.JoinHostPort(c.host, strconv.Itoa(c.port)) }

// fingerprint: the inbound's own choice, then the panel's default. A malformed value saved
// before the panel checked them falls through rather than reaching the apps.
func (c *clientBuilder) fingerprint() string {
	for _, fp := range []string{c.ext.Client.Fingerprint, c.in.Fingerprint} {
		if ValidFingerprint(fp) {
			return fp
		}
	}
	return DefaultFingerprint
}

// sniKey: vless and vmess call the TLS name "servername", everything else "sni".
func (c *clientBuilder) sniKey() string {
	if typ := c.t.Type(); typ == "vless" || typ == "vmess" {
		return "servername"
	}
	return "sni"
}

func (c *clientBuilder) security() error {
	if r := c.t.section("reality-config"); r != nil {
		pbk, err := RealityPublicKey(str(r["private-key"]))
		if err != nil {
			return err
		}
		sni := c.ext.Client.SNI
		if sni == "" {
			sni = strings1(r["server-names"])[0]
		}
		sid := strings1(r["short-id"])[0]
		c.y["tls"] = true
		c.y[c.sniKey()] = sni
		c.y["client-fingerprint"] = c.fingerprint()
		c.y["reality-opts"] = map[string]any{"public-key": pbk, "short-id": sid}
		for k, v := range map[string]string{"security": "reality", "sni": sni, "fp": c.fingerprint(), "pbk": pbk, "sid": sid} {
			c.q.Set(k, v)
		}
		return nil
	}
	if !rules[c.t.Type()].cert && c.ext.TLS != "node" {
		return nil
	}
	sni := c.ext.Client.SNI
	if sni == "" {
		sni = c.in.SNI
	}
	typ := c.t.Type()
	switch typ {
	case "vless", "vmess", "trojan", "anytls":
		c.y["tls"] = true
		c.y["client-fingerprint"] = c.fingerprint()
		c.q.Set("security", "tls")
		c.q.Set("fp", c.fingerprint())
	case "trusttunnel": // always TLS, no switch for it
		c.y["client-fingerprint"] = c.fingerprint()
	}
	if typ == "hysteria2" || typ == "tuic" {
		delete(c.y, "tls")
	}
	if sni != "" {
		c.y[c.sniKey()] = sni
		c.q.Set("sni", sni)
	}
	if c.in.PinSHA256 != "" {
		// A self-signed certificate is pinned. Links carry the pin where the app reads
		// one — Hysteria2's pinSHA256, Xray's pcs (pinnedPeerCertSha256) — next to the
		// insecure flag they need to accept a certificate without a CA at all.
		c.y["fingerprint"] = c.in.PinSHA256
		c.y["skip-cert-verify"] = false
		switch typ {
		case "hysteria2":
			c.q.Set("insecure", "1")
			c.q.Set("pinSHA256", c.in.PinSHA256)
		case "tuic":
			c.q.Set("allow_insecure", "1") // the TUIC link scheme has no pin
		default:
			c.q.Set("allowInsecure", "1")
			c.q.Set("insecure", "1")
			c.q.Set("pcs", c.in.PinSHA256)
		}
	}
	return nil
}

func (c *clientBuilder) transport() {
	typ := c.t.Type()
	if typ != "vless" && typ != "vmess" && typ != "trojan" {
		return
	}
	switch transport(c.t) {
	case "xhttp":
		x := c.t.section("xhttp-config")
		opts := map[string]any{"reuse-settings": xmux}
		for _, k := range []string{"path", "mode", "host"} {
			if v, ok := x[k].(string); ok && v != "" {
				opts[k] = v
				c.q.Set(k, v)
			}
		}
		// Padding, session and uplink settings must match on both ends: a client on the
		// defaults gets "400 invalid xpadding" from a node with its own x-padding-bytes.
		extra := map[string]any{}
		for _, k := range xhttpShared {
			if v, ok := x[k.mihomo]; ok && v != nil {
				opts[k.mihomo], extra[k.xray] = v, v
			}
		}
		// The admin's own reuse settings replace the panel's: they are one setting, and
		// max-connections cannot go with the default max-concurrency.
		if cl := xhttpClient(c.t); cl != nil {
			if own, _ := cl["xmux"].(map[string]any); len(own) > 0 {
				reuse, link := map[string]any{}, map[string]any{}
				for _, k := range xmuxKeys {
					if v := scalar(own[k.mihomo]); v != "" {
						reuse[k.mihomo], link[k.xray] = v, v
					}
				}
				opts["reuse-settings"], extra["xmux"] = reuse, link
			}
			if v := scalar(cl["sc-min-posts-interval-ms"]); v != "" {
				opts["sc-min-posts-interval-ms"], extra["scMinPostsIntervalMs"] = v, v
			}
			// Without a length Xray finds the table "too small" where mihomo draws 16-32.
			if v := scalar(cl["session-table"]); v != "" {
				l := scalar(cl["session-length"])
				if l == "" {
					l = defaultSessionLength
				}
				opts["session-table"], extra["sessionIDTable"] = v, v
				opts["session-length"], extra["sessionIDLength"] = l, l
			}
		}
		if len(extra) > 0 {
			b, _ := json.Marshal(extra)
			c.q.Set("extra", string(b))
		}
		c.y["network"], c.y["xhttp-opts"] = "xhttp", opts
		c.q.Set("type", "xhttp")
	case "grpc":
		name := c.t.str("grpc-service-name")
		c.y["network"], c.y["grpc-opts"] = "grpc", map[string]any{"grpc-service-name": name}
		c.q.Set("type", "grpc")
		c.q.Set("serviceName", name)
		c.q.Set("mode", "gun")
	case "ws":
		path := c.t.str("ws-path")
		c.y["network"], c.y["ws-opts"] = "ws", map[string]any{"path": path}
		c.q.Set("type", "ws")
		c.q.Set("path", path)
	default:
		c.y["network"] = "tcp"
		c.q.Set("type", "tcp")
	}
	// mihomo carries UDP over VLESS/VMess/Trojan streams, but not over XHTTP in v1.19.31.
	c.y["udp"] = transport(c.t) != "xhttp"
}

func (c *clientBuilder) finish() (Client, error) {
	s := c.in.Slot
	name := url.PathEscape(c.in.Name)
	switch c.t.Type() {
	case "vless":
		c.y["uuid"] = s.UUID
		c.q.Set("encryption", "none")
		if c.t.hasEncryption() {
			enc, err := ClientEncryption(c.t.str("decryption"))
			if err != nil {
				return Client{}, err
			}
			c.y["encryption"] = enc
			c.q.Set("encryption", enc)
		}
		if c.ext.Flow != "" {
			c.y["flow"] = c.ext.Flow
			c.q.Set("flow", c.ext.Flow)
		}
		return Client{c.y, "vless://" + s.UUID + "@" + c.addr() + "?" + c.q.Encode() + "#" + name}, nil
	case "vmess":
		c.y["uuid"], c.y["alterId"], c.y["cipher"] = s.UUID, 0, "auto"
		link := map[string]string{"v": "2", "ps": c.in.Name, "add": c.host, "port": strconv.Itoa(c.port), "id": s.UUID, "aid": "0", "scy": "auto",
			"net": c.q.Get("type"), "type": "none", "path": c.q.Get("path"), "tls": c.q.Get("security"), "sni": c.q.Get("sni"), "fp": c.q.Get("fp"),
			"pbk": c.q.Get("pbk"), "sid": c.q.Get("sid"), "pcs": c.q.Get("pcs")}
		if link["net"] == "grpc" {
			link["path"] = c.q.Get("serviceName")
		}
		b, _ := json.Marshal(link)
		return Client{c.y, "vmess://" + base64.StdEncoding.EncodeToString(b)}, nil
	case "trojan":
		c.y["password"] = s.Secret
		return Client{c.y, "trojan://" + url.PathEscape(s.Secret) + "@" + c.addr() + "?" + c.q.Encode() + "#" + name}, nil
	case "hysteria2":
		c.y["password"] = s.Secret
		if alpn := strings1(c.t["alpn"]); len(alpn) > 0 {
			c.y["alpn"] = alpn
		}
		// A port of the inbound's own (a proxy in front) replaces the node's hopping range.
		if c.in.PortSpec != "" && c.in.PortSpec != strconv.Itoa(c.in.Port) && c.port == c.in.Port {
			c.y["ports"] = c.in.PortSpec
			c.q.Set("mport", c.in.PortSpec)
		}
		if obfs := c.t.str("obfs"); obfs != "" {
			c.y["obfs"], c.y["obfs-password"] = obfs, c.t.str("obfs-password")
			c.q.Set("obfs", obfs)
			c.q.Set("obfs-password", c.t.str("obfs-password"))
			// Gecko's sizes are the sender's own; the client gets the server's.
			for _, key := range []string{"obfs-min-packet-size", "obfs-max-packet-size"} {
				if n, ok := toInt(c.t[key]); ok {
					c.y[key] = n
				}
			}
		}
		return Client{c.y, "hysteria2://" + url.PathEscape(s.Secret) + "@" + c.addr() + "/?" + c.q.Encode() + "#" + name}, nil
	case "tuic":
		cc := c.t.str("congestion-controller")
		if cc == "" {
			cc = "bbr"
		}
		alpn := strings1(c.t["alpn"])
		if len(alpn) == 0 {
			alpn = []string{"h3"}
		}
		c.y["uuid"], c.y["password"], c.y["alpn"] = s.UUID, s.Secret, alpn
		c.y["congestion-controller"], c.y["udp-relay-mode"] = cc, "native"
		c.q.Set("congestion_control", cc)
		c.q.Set("alpn", alpn[0])
		c.q.Set("udp_relay_mode", "native")
		return Client{c.y, "tuic://" + s.UUID + ":" + url.PathEscape(s.Secret) + "@" + c.addr() + "?" + c.q.Encode() + "#" + name}, nil
	case "anytls":
		c.y["password"], c.y["udp"] = s.Secret, true
		c.q.Del("security")
		c.q.Del("fp")
		return Client{c.y, "anytls://" + url.PathEscape(s.Secret) + "@" + c.addr() + "/?" + c.q.Encode() + "#" + name}, nil
	}
	if cl, ok := c.finishExtra(); ok {
		return cl, nil
	}
	return Client{}, fail("config_type", "type")
}
