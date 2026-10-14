// Package proto describes inbounds as mihomo listener templates. A template is a mihomo
// listener config without the fields mikan manages: the node fills in name, port,
// listen, users and the certificate, and subscriptions derive client configs from the
// same template. mikan's own settings live under the "mikan" key and never reach mihomo.
package proto

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Template is a parsed listener template: mihomo keys plus the "mikan" section.
type Template map[string]any

const extKey = "mikan"

// Ext is the "mikan" section.
type Ext struct {
	Flow   string          `json:"flow,omitempty" yaml:"flow,omitempty"` // vless: xtls-rprx-vision
	TLS    string          `json:"tls,omitempty" yaml:"tls,omitempty"`   // "node": use the node certificate
	Client ClientOverrides `json:"client,omitzero" yaml:"client,omitempty"`
}

// ClientOverrides change what clients get without touching the server side, e.g. a CDN
// address in front of the node.
type ClientOverrides struct {
	Server      string `json:"server,omitempty" yaml:"server,omitempty"`
	Port        int    `json:"port,omitempty" yaml:"port,omitempty"`
	SNI         string `json:"sni,omitempty" yaml:"sni,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty" yaml:"fingerprint,omitempty"` // uTLS profile (Fingerprints); the panel's default when empty
}

// Error is a validation failure: Code is stable (the UI translates it), Field names the
// offending key.
type Error struct {
	Code   string
	Field  string
	Detail string
}

func (e *Error) Error() string {
	s := e.Code
	if e.Field != "" {
		s += " (" + e.Field + ")"
	}
	if e.Detail != "" {
		s += ": " + e.Detail
	}
	return s
}

func fail(code, field string) error { return &Error{Code: code, Field: field} }

type rule struct {
	keys    []string // allowed top-level keys besides type and mikan
	network string   // tcp | udp
	cert    bool     // always runs on the node certificate
	secured bool     // needs reality-config or mikan.tls: node
	shared  bool     // one key for everyone: no per-user accounting or limits (see extra.go)
}

// Listener types that authenticate every user: the node counts traffic and enforces
// limits per user. The shared ones are the exception the admin opts into.
var rules = map[string]rule{
	"vless":  {keys: []string{"ws-path", "grpc-service-name", "xhttp-config", "reality-config", "mux-option", "decryption"}, network: "tcp", secured: true},
	"vmess":  {keys: []string{"ws-path", "grpc-service-name", "reality-config", "mux-option"}, network: "tcp", secured: true},
	"trojan": {keys: []string{"ws-path", "grpc-service-name", "reality-config", "mux-option"}, network: "tcp", secured: true},
	"hysteria2": {keys: []string{"obfs", "obfs-password", "obfs-min-packet-size", "obfs-max-packet-size", "max-idle-time", "alpn", "up", "down",
		"ignore-client-bandwidth", "masquerade", "cwnd", "bbr-profile", "udp-mtu", "initial-stream-receive-window", "max-stream-receive-window",
		"initial-connection-receive-window", "max-connection-receive-window"}, network: "udp", cert: true},
	"tuic":        {keys: []string{"congestion-controller", "max-idle-time", "authentication-timeout", "alpn", "max-udp-relay-packet-size", "cwnd", "bbr-profile"}, network: "udp", cert: true},
	"anytls":      {keys: []string{"padding-scheme"}, network: "tcp", cert: true},
	"trusttunnel": {keys: []string{"congestion-controller", "cwnd", "bbr-profile"}, network: "tcp", cert: true},
	"shadowquic": {keys: []string{"jls-upstream", "alpn", "quic-versions", "congestion-controller", "up", "down", "ignore-client-bandwidth",
		"max-idle-time", "cwnd", "bbr-profile", "max-datagram-frame-size", "recv-window-conn", "recv-window", "disable-mtu-discovery"}, network: "udp"},
	"mieru":       {keys: []string{"transport"}, network: "tcp"},
	"shadowsocks": {keys: []string{"cipher", "password"}, network: "tcp", shared: true},
	"sudoku": {keys: []string{"key", "aead-method", "padding-min", "padding-max", "table-type", "handshake-timeout", "enable-pure-downlink", "httpmask"},
		network: "tcp", shared: true},
	"snell": {keys: []string{"psk", "version", "obfs-opts"}, network: "tcp", shared: true},
}

// managed keys are set by the node; the rest of the forbidden list would let a template
// route around the node's REJECT rules or read files on the server.
var managed = []string{"name", "port", "listen", "users", "certificate", "private-key"}

// Parse reads a YAML (or JSON) template.
func Parse(src string) (Template, error) {
	var t Template
	if err := yaml.Unmarshal([]byte(src), &t); err != nil {
		return nil, &Error{Code: "config_yaml", Detail: yamlDetail(err)}
	}
	if t == nil {
		return nil, fail("config_empty", "")
	}
	return Template(normalize(t).(map[string]any)), nil
}

// FromJSON reads the template the panel ships to the node.
func FromJSON(raw []byte) (Template, error) {
	var t Template
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, &Error{Code: "config_yaml", Detail: err.Error()}
	}
	return t, nil
}

func yamlDetail(err error) string {
	s := err.Error()
	return strings.TrimPrefix(s, "yaml: ")
}

// normalize makes every nested mapping a plain map[string]any: yaml.v3 decodes nested
// mappings into the named type of the outer map (Template here), and non-string keys
// into map[any]any.
func normalize(v any) any {
	switch x := v.(type) {
	case Template:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[k] = normalize(e)
		}
		return m
	case map[string]any:
		for k, e := range x {
			x[k] = normalize(e)
		}
		return x
	case map[any]any:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[fmt.Sprint(k)] = normalize(e)
		}
		return m
	case []any:
		for i, e := range x {
			x[i] = normalize(e)
		}
	}
	return v
}

// Clone is a deep copy: the maps and lists of a template are its own.
func (t Template) Clone() Template {
	return Template(deepCopy(map[string]any(t)).(map[string]any))
}

func deepCopy(v any) any {
	switch x := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[k] = deepCopy(e)
		}
		return m
	case []any:
		l := make([]any, len(x))
		for i, e := range x {
			l[i] = deepCopy(e)
		}
		return l
	case []string:
		return append([]string(nil), x...)
	}
	return v
}

func (t Template) Type() string {
	s, _ := t["type"].(string)
	return s
}

// Network is the transport the listener binds: two templates may share a port number
// only on different networks.
func (t Template) Network() string {
	return rules[t.Type()].network
}

// NodeCert says whether the listener runs on the node's certificate: Hysteria2, TUIC,
// AnyTLS, TrustTunnel, and VLESS with TLS on the node certificate.
func (t Template) NodeCert() bool {
	return rules[t.Type()].cert || t.Ext().TLS == "node"
}

func (t Template) Ext() Ext {
	var e Ext
	if raw, ok := t[extKey]; ok {
		b, _ := json.Marshal(raw)
		_ = json.Unmarshal(b, &e)
	}
	return e
}

func (t Template) section(key string) map[string]any {
	m, _ := t[key].(map[string]any)
	return m
}

func (t Template) str(key string) string {
	s, _ := t[key].(string)
	return s
}

// Validate checks a template against the per-type allow list and mikan's rules. The node
// still parses it with mihomo before applying (see node.Validate).
func Validate(t Template, o Options) error {
	typ := t.Type()
	r, ok := rules[typ]
	if !ok {
		return &Error{Code: "config_type", Field: "type", Detail: typ}
	}
	for k := range t {
		switch {
		case k == "type" || k == extKey:
		case slices.Contains(managed, k):
			return fail("config_managed", k)
		case !slices.Contains(r.keys, k):
			return fail("config_key", k)
		}
	}
	if raw, ok := t[extKey]; ok {
		m, isMap := raw.(map[string]any)
		if !isMap {
			return fail("config_key", extKey)
		}
		for k := range m {
			if k != "flow" && k != "tls" && k != "client" {
				return fail("config_key", extKey+"."+k)
			}
		}
	}
	ext := t.Ext()
	if ext.TLS != "" && ext.TLS != "node" {
		return fail("config_tls", extKey+".tls")
	}
	reality := t.section("reality-config")
	if r.secured {
		if reality == nil && ext.TLS != "node" {
			return fail("config_insecure", "reality-config")
		}
		if reality != nil && ext.TLS == "node" {
			return fail("config_both_tls", extKey+".tls")
		}
	}
	if reality != nil {
		if err := validateReality(reality, o); err != nil {
			return err
		}
	}
	if ext.Flow != "" {
		if typ != "vless" || ext.Flow != "xtls-rprx-vision" || transport(t) != "tcp" {
			return fail("config_flow", extKey+".flow")
		}
	}
	if x := t.section("xhttp-config"); x != nil {
		if mode, _ := x["mode"].(string); mode != "" && mode != "stream-one" && mode != "stream-up" && mode != "packet-up" {
			// "auto" hangs with mihomo v1.19.31 on both ends (S-01a).
			return fail("config_xhttp_mode", "xhttp-config.mode")
		}
		if p, _ := x["path"].(string); p != "" && !strings.HasPrefix(p, "/") {
			return fail("config_path", "xhttp-config.path")
		}
		// The same checks as the form's: a value the core refuses would only show when the
		// node loads the inbound.
		if tune, ok := XHTTPOf(t); ok {
			if err := tune.check(); err != nil {
				return err
			}
		}
	}
	if p := t.str("ws-path"); t["ws-path"] != nil && !strings.HasPrefix(p, "/") {
		return fail("config_path", "ws-path")
	}
	if m, ok := t["masquerade"]; ok {
		s, _ := m.(string)
		if !publicHTTPS(s) {
			return fail("config_masquerade", "masquerade")
		}
	}
	if typ == "hysteria2" {
		if err := validateObfs(t); err != nil {
			return err
		}
	}
	if _, set := t["decryption"]; set {
		if _, isString := t["decryption"].(string); !isString {
			return fail("vless_decryption", "decryption")
		}
		if t.hasEncryption() {
			if _, err := parseDecryption(t.str("decryption")); err != nil {
				return err
			}
		}
	}
	if o := ext.Client; o.Port < 0 || o.Port > 65535 {
		return fail("config_key", extKey+".client.port")
	}
	return validateExtra(t, o)
}

// transport is what a client dials over the secured channel.
func transport(t Template) string {
	switch {
	case t["xhttp-config"] != nil:
		return "xhttp"
	case t["grpc-service-name"] != nil:
		return "grpc"
	case t["ws-path"] != nil:
		return "ws"
	}
	return "tcp"
}

// Marshal renders a template as YAML with a stable, readable key order: type first, the
// transport and security next, mikan's section last.
func Marshal(t Template) string {
	order := func(k string) int {
		switch k {
		case "type":
			return 0
		case "ws-path", "grpc-service-name", "xhttp-config":
			return 1
		case "reality-config":
			return 2
		case extKey:
			return 9
		}
		return 5
	}
	keys := make([]string, 0, len(t))
	for k := range t {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if oi, oj := order(keys[i]), order(keys[j]); oi != oj {
			return oi < oj
		}
		return keys[i] < keys[j]
	})
	root := &yaml.Node{Kind: yaml.MappingNode}
	for _, k := range keys {
		var v yaml.Node
		_ = v.Encode(t[k])
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: k}, &v)
	}
	out, _ := yaml.Marshal(root)
	return string(out)
}

// JSON is the wire form the panel sends to the node.
func (t Template) JSON() json.RawMessage {
	b, _ := json.Marshal(t)
	return b
}

// Hysteria2 obfuscation: Salamander scrambles every packet; Gecko (mihomo 1.19.26+) also
// cuts QUIC handshake packets into padded fragments of random size, against DPI that
// matches on packet sizes.
const (
	ObfsSalamander = "salamander"
	ObfsGecko      = "gecko"
	// Gecko's packet sizes on the wire; its own limit for one fragment is 2048 bytes.
	GeckoMinSize = 256
	GeckoMaxSize = 2048
)

func validateObfs(t Template) error {
	obfs := t.str("obfs")
	_, minSet := t["obfs-min-packet-size"]
	_, maxSet := t["obfs-max-packet-size"]
	switch {
	case obfs == "" && t["obfs"] == nil:
		if minSet || maxSet {
			return fail("config_obfs", "obfs")
		}
		return nil
	case obfs != ObfsSalamander && obfs != ObfsGecko, t.str("obfs-password") == "":
		return fail("config_obfs", "obfs")
	case obfs == ObfsSalamander && (minSet || maxSet):
		return fail("config_obfs_sizes", "obfs-min-packet-size")
	}
	lo, hi := 512, 1200 // Gecko's defaults
	for key, dst := range map[string]*int{"obfs-min-packet-size": &lo, "obfs-max-packet-size": &hi} {
		raw, set := t[key]
		if !set {
			continue
		}
		n, ok := toInt(raw)
		if !ok || n < GeckoMinSize || n > GeckoMaxSize {
			return fail("config_obfs_sizes", key)
		}
		*dst = n
	}
	if lo > hi {
		return fail("config_obfs_sizes", "obfs-min-packet-size")
	}
	return nil
}

// Obfs is a Hysteria2 template's obfuscation, "" without one.
func Obfs(t Template) string {
	if t.Type() != "hysteria2" {
		return ""
	}
	return t.str("obfs")
}

// SetObfs switches a Hysteria2 template to Salamander or Gecko. The password stays (the
// two share it); password fills one in when the template has none. Gecko's packet sizes
// go away with Gecko.
func SetObfs(t Template, obfs, password string) error {
	if t.Type() != "hysteria2" || (obfs != ObfsSalamander && obfs != ObfsGecko) {
		return fail("config_obfs", "obfs")
	}
	t["obfs"] = obfs
	if t.str("obfs-password") == "" {
		t["obfs-password"] = password
	}
	if obfs != ObfsGecko {
		delete(t, "obfs-min-packet-size")
		delete(t, "obfs-max-packet-size")
	}
	return validateObfs(t)
}
