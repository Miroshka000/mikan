// Package subtest holds what the tests of the subscription renderers share: the users,
// nodes and inbounds the golden files are made of, and the check of a golden file.
//
// The golden files pin what a subscription looks like byte for byte: the same users in
// every routing mode, as the Clash profile (mihomo), the sing-box config (singbox) and the
// share links (xray), each next to its renderer under testdata/golden. They exist so that
// the renderers can be reshaped without a profile of any subscriber changing unnoticed. A
// change that is meant to alter a profile regenerates the files
//
//	go test ./internal/sub/mihomo ./internal/sub/singbox ./internal/sub/xray -run Golden -update
//
// and the diff of the files is its review.
package subtest

import (
	"bytes"
	"encoding/base64"
	"flag"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"mikan/internal/proto"
	"mikan/internal/sub/apps"
	"mikan/internal/sub/build"
	"mikan/internal/sub/model"
	"mikan/internal/sub/routing"
)

var update = flag.Bool("update", false, "rewrite the golden files of the subscription profiles")

// goldenDir holds the expected outputs, in the directory of the package under test.
const goldenDir = "testdata/golden"

// Golden compares got with the file name under testdata/golden, or writes the file with
// -update.
func Golden(t *testing.T, name string, got []byte) {
	t.Helper()
	file := filepath.Join(goldenDir, name)
	if *update {
		if err := os.MkdirAll(goldenDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("%v (run with -update to write the golden files)", err)
	}
	// A checkout on Windows may turn the files' line ends into CRLF.
	want = bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from the golden file (run with -update if the change is meant):\n--- got\n%s\n--- want\n%s", name, got, want)
	}
}

// NodeTarget sends a service to one node, the way the admin panel writes the target.
func NodeTarget(id int64) string { return "node:" + strconv.FormatInt(id, 10) }

// User is a test user: what its proxies are made of, and the panel's settings around them.
type User struct {
	Build  build.Profile
	Direct []string // the panel's and the nodes' hosts
	// Rules are the admin's own Clash rules, before a core leaves out the ones an app's
	// core would not parse.
	Rules  []routing.Rule
	Routes routing.Routes
	Lang   string
}

// Proxies are the user's proxies.
func (u User) Proxies() []model.Proxy { return build.Proxies(u.Build) }

// Keys are the user's keys, the same in every run.
var Keys = proto.Slot{Name: "s000001", UUID: "0b4ddc4c-7c4f-4a36-9d62-6f1a44b8c4e1", Secret: "S3cr3t+/="}

// realityKey is a REALITY private key that is the same in every run: the bytes 1 to 32,
// clamped like a generated one, so the public key the profiles carry is fixed as well.
func realityKey() string {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i + 1)
	}
	k[0] &= 248
	k[31] &= 127
	k[31] |= 64
	return base64.RawURLEncoding.EncodeToString(k)
}

func reality(sni string) map[string]any {
	return map[string]any{"dest": sni + ":443", "server-names": []any{sni}, "private-key": realityKey(), "short-id": []any{"a1b2c3d4"}}
}

// Inbound is an inbound of a preset as the panel's presets make it, with the parts they
// make at random (paths, passwords, keys) fixed: name is the preset's name in subscriptions.
func Inbound(id, node int64, name string, port int, tmpl proto.Template) model.Inbound {
	return model.Inbound{ID: id, Node: node, Label: name, Name: name, Port: port, PortSpec: strconv.Itoa(port), Config: proto.Marshal(tmpl)}
}

// Inbounds are one of each kind the profiles carry, on the node: the default presets and,
// with more, the ones of other families.
func Inbounds(node, firstID int64, more bool) []model.Inbound {
	out := []model.Inbound{
		Inbound(firstID, node, "VLESS XHTTP", 443, proto.Template{"type": "vless", "xhttp-config": map[string]any{"path": "/golden-path", "mode": "stream-one"}, "reality-config": reality("www.example.com")}),
		Inbound(firstID+1, node, "Hysteria2", 443, proto.Template{"type": "hysteria2", "alpn": []any{"h3"}, "obfs": proto.ObfsSalamander, "obfs-password": "golden-obfs-password"}),
		Inbound(firstID+2, node, "TUIC", 8443, proto.Template{"type": "tuic", "alpn": []any{"h3"}, "congestion-controller": "bbr", "max-idle-time": 15000, "authentication-timeout": 1000}),
		Inbound(firstID+3, node, "VLESS Vision", 8443, proto.Template{"type": "vless", "mikan": map[string]any{"flow": "xtls-rprx-vision"}, "reality-config": reality("www.example.com")}),
	}
	if more {
		out = append(out,
			Inbound(firstID+4, node, "Trojan", 2087, proto.Template{"type": "trojan", "reality-config": reality("www.example.com")}),
			Inbound(firstID+5, node, "AnyTLS", 2083, proto.Template{"type": "anytls"}),
			Inbound(firstID+6, node, "VLESS gRPC", 2053, proto.Template{"type": "vless", "grpc-service-name": "goldengrpc", "reality-config": reality("www.example.com")}),
		)
	}
	return out
}

// Defaults is a user of one node with the default presets; pin is the node's self-signed
// certificate, "" for a public one.
func Defaults(pin string) User {
	return User{Build: build.Profile{Keys: Keys, Inbounds: Inbounds(1, 1, false),
		Nodes: []model.Node{{ID: 1, Endpoint: model.Endpoint{Host: "203.0.113.7", PinSHA256: pin}}}}}
}

// TwoNodes is the user of Defaults on node 1 and a second node in the US with XHTTP.
func TwoNodes() User {
	u := Defaults("")
	u.Build.Nodes = []model.Node{{ID: 1, Name: "🇳🇱 Нидерланды", Endpoint: model.Endpoint{Host: "203.0.113.7"}}, {ID: 2, Name: "🇺🇸 США", Endpoint: model.Endpoint{Host: "198.51.100.9"}}}
	u.Build.Inbounds = append(u.Build.Inbounds, Inbounds(2, 100, false)[0])
	return u
}

// The nodes' certificate pins: a SHA-256 in hex, as the panel keeps it and the apps take
// it. Xray leaves out a self-signed server without a real one.
var (
	PinSingle = strings.Repeat("ab12", 16)
	PinNL     = strings.Repeat("aa11", 16)
	PinUS     = strings.Repeat("bb22", 16)
)

// Single is a subscription of one node with a pinned self-signed certificate.
func Single() User {
	return User{
		Build: build.Profile{Keys: Keys, Inbounds: Inbounds(1, 1, false),
			Nodes: []model.Node{{ID: 1, Endpoint: model.Endpoint{Host: "203.0.113.7", PinSHA256: PinSingle}}}},
		Direct: []string{"203.0.113.7", "vpn.example.com"},
	}
}

// Multi is a subscription of two nodes: the second with a domain and its own certificate, a
// renamed inbound, a default fingerprint, and an inbound of a node the user is not given.
func Multi() User {
	ins := Inbounds(1, 1, true)
	us := Inbounds(2, 100, false)
	us[0].Name, us[0].Named = "Особый", true
	ins = append(ins, us...)
	other := ins[1]
	other.ID, other.Node, other.Label = 200, 3, "x"
	ins = append(ins, other)
	return User{
		Build: build.Profile{Keys: Keys, Inbounds: ins, Fingerprint: "chrome",
			Nodes: []model.Node{
				{ID: 1, Name: "🇳🇱 Нидерланды", Endpoint: model.Endpoint{Host: "203.0.113.7", PinSHA256: PinNL}},
				{ID: 2, Name: "🇺🇸 США", Endpoint: model.Endpoint{Host: "usa.example.com", SNI: "usa.example.com", PinSHA256: PinUS}},
			}},
		Direct: []string{"203.0.113.7", "usa.example.com", "vpn.example.com"},
	}
}

// Routes is the admin's routing with a bit of everything: services sent three ways and to
// a node, direct sets, own lists, the DNS, the finer settings.
func Routes() routing.Routes {
	return routing.Routes{
		Services: map[string]string{"youtube": routing.TargetVPN, "ads": routing.TargetBlock, "torrents": routing.TargetDirect, "ai": NodeTarget(2), "telegram": routing.TargetVPN},
		Direct:   []string{"ru"},
		Lists: []routing.RouteList{
			{Name: "wl", URL: "https://example.com/wl.yaml", Behavior: "classical", Format: "yaml", Target: routing.TargetVPN},
			{Name: "ru-ip", URL: "https://example.com/ru.txt", Behavior: "ipcidr", Format: "text", Target: routing.TargetDirect},
			{Name: "ads", URL: "https://example.com/ads.mrs", Behavior: "domain", Format: "mrs", Target: routing.TargetBlock},
		},
		DNS:     routing.DNS{Nameserver: []string{"https://dns.alidns.com/dns-query"}, Policy: []routing.DNSPolicy{{Match: "+.cn", Servers: []string{"223.5.5.5"}}}},
		Servers: routing.Servers{Auto: "fallback", Interval: 120},
		Tune:    routing.Tune{BlockQUIC: true, Sniffer: true},
	}
}

// Rules are the admin's own Clash rules; the DOMAIN-WILDCARD one is younger than the cores
// of some apps.
var Rules = ParseRules("DOMAIN-SUFFIX,sber.ru,DIRECT", "DOMAIN-WILDCARD,*.tinkoff.ru,DIRECT", "IP-CIDR,10.0.0.0/8,DIRECT,no-resolve")

// ParseRules are the admin's rules of the lines, to the groups of the default names; a line
// that does not parse is a mistake of the test.
func ParseRules(lines ...string) []routing.Rule {
	rules, err := routing.ParseRules(strings.Join(lines, "\n"), model.GroupNames{}.WithDefaults(""))
	if err != nil {
		panic(err)
	}
	return rules
}

// Modes are the routing modes every profile is rendered in.
var Modes = []routing.Mode{routing.All, routing.RUDirect, routing.Blocked}

// Variant is one user of the golden files: one node or two, with the admin's settings or
// without, in an app.
type Variant struct {
	Name   string // the golden files' prefix
	Base   func() User
	Custom bool // the admin's Routes and Rules, and the panel in English
	App    apps.App
	Groups model.GroupNames
}

// User is the variant's user.
func (v Variant) User() User {
	u := v.Base()
	if v.Custom {
		u.Rules, u.Routes, u.Lang = Rules, Routes(), "en"
	}
	return u
}

// Variants are the users of the golden files; the last takes no rule lists.
var Variants = []Variant{
	{"single-plain", Single, false, apps.App{}, model.GroupNames{}},
	{"single-custom", Single, true, apps.App{Family: apps.FamilyMihomo, Core: apps.Version{1, 19, 32}}, model.GroupNames{Main: "Прокси", Auto: "Fast"}},
	{"multi-plain", Multi, false, apps.App{}, model.GroupNames{}},
	{"multi-custom", Multi, true, apps.App{Family: apps.FamilyMihomo, Core: apps.Version{1, 19, 32}}, model.GroupNames{Main: "Прокси", Auto: "Fast"}},
	{"multi-old-core", Multi, true, apps.App{Family: apps.FamilyMihomo, Core: apps.Version{1, 18, 0}}, model.GroupNames{}},
	{"multi-stash", Multi, true, apps.App{Family: apps.FamilyStash}, model.GroupNames{}},
}

// Clash is the admin's own Clash profile src as the template it is.
func Clash(src string) model.Template { return model.Template{Core: model.CoreMihomo, Body: src} }

// OwnSingBoxTemplate is an admin's own sing-box template with groups of each kind of ask
// (one left with no server for the two-node user), a rule set downloaded through the main
// group and DNS through it.
const OwnSingBoxTemplate = `{
  "log": {"level": "info"},
  "dns": {
    "servers": [
      {"type": "https", "tag": "remote", "server": "1.1.1.1", "detour": "🌍 Сеть"},
      {"type": "local", "tag": "local"}
    ],
    "final": "remote"
  },
  "inbounds": [{"type": "tun", "tag": "tun-in", "address": ["172.19.0.1/30"], "auto_route": true, "strict_route": true}],
  "outbounds": [
    {"type": "selector", "tag": "🌍 Сеть", "outbounds": ["⚡ Авто", "🇺🇸 США", "🔒 VLESS", "🇯🇵 Япония", "direct"]},
    {"type": "urltest", "tag": "⚡ Авто", "mikan": {}, "url": "https://www.gstatic.com/generate_204", "interval": "5m"},
    {"type": "selector", "tag": "🇺🇸 США", "mikan": {"filter": "🇺🇸"}},
    {"type": "selector", "tag": "🇯🇵 Япония", "mikan": {"filter": "🇯🇵"}},
    {"type": "selector", "tag": "🔒 VLESS", "mikan": {"nodes": ["🇳🇱 Нидерланды"], "types": ["vless"]}},
    {"type": "direct", "tag": "direct"}
  ],
  "route": {
    "rule_set": [
      {"type": "remote", "tag": "geosite-ru", "format": "binary", "url": "https://example.com/geosite-ru.srs", "download_detour": "🌍 Сеть"}
    ],
    "rules": [
      {"action": "sniff"},
      {"protocol": "dns", "action": "hijack-dns"},
      {"rule_set": "geosite-ru", "outbound": "direct"},
      {"network": "udp", "port": 443, "action": "reject"}
    ],
    "final": "🌍 Сеть",
    "auto_detect_interface": true,
    "default_domain_resolver": "local"
  }
}
`

// OwnXrayTemplate is an admin's own xray template: the automatic choice with a config of
// its own, a balancer of one node's VLESS servers a service goes to, one left with no
// server for the two-node user, and DNS and the local networks as the apps need them.
const OwnXrayTemplate = `{
  "log": {"loglevel": "warning"},
  "dns": {"tag": "dns", "servers": ["https://1.1.1.1/dns-query"]},
  "inbounds": [
    {"tag": "socks", "protocol": "socks", "listen": "127.0.0.1", "port": 10808, "settings": {"auth": "noauth", "udp": true},
     "sniffing": {"enabled": true, "destOverride": ["http", "tls", "quic"]}}
  ],
  "outbounds": [
    {"tag": "direct", "protocol": "freedom"},
    {"tag": "dns-out", "protocol": "dns"}
  ],
  "routing": {
    "domainStrategy": "AsIs",
    "rules": [
      {"port": "53", "outboundTag": "dns-out"},
      {"ip": ["geoip:private"], "outboundTag": "direct"},
      {"domain": ["geosite:youtube"], "balancerTag": "🔒 VLESS"},
      {"domain": ["geosite:openai"], "balancerTag": "🇯🇵 Япония"},
      {"network": "tcp,udp", "outboundTag": "proxy"}
    ],
    "balancers": [
      {"tag": "⚡ Авто", "mikan": {"choice": true}, "strategy": {"type": "leastPing"}},
      {"tag": "🔒 VLESS", "mikan": {"nodes": ["🇳🇱 Нидерланды"], "types": ["vless"]}, "strategy": {"type": "random"}},
      {"tag": "🇯🇵 Япония", "mikan": {"filter": "🇯🇵"}, "strategy": {"type": "random"}}
    ]
  },
  "observatory": {"subjectSelector": ["proxy"], "probeUrl": "https://www.gstatic.com/generate_204", "probeInterval": "5m"}
}
`

// OwnTemplate is an admin's own Clash profile with groups of each kind of ask.
const OwnTemplate = `mode: rule
dns:
  enable: true
  enhanced-mode: fake-ip
proxy-groups:
  - name: 🌍 Сеть
    type: select
    proxies: [⚡ Авто, 🇺🇸 США, 🚫 Без VPN]
  - name: ⚡ Авто
    type: url-test
    include-all-proxies: true
    exclude-type: hysteria2
    url: https://www.gstatic.com/generate_204
    interval: 300
  - name: 🇺🇸 США
    type: select
    include-all-proxies: true
    filter: "🇺🇸"
  - name: 🇯🇵 Япония
    type: select
    include-all-proxies: true
    filter: "🇯🇵"
  - name: 🔒 VLESS
    type: select
    mikan: {nodes: ["🇳🇱 Нидерланды"], types: [vless]}
  - name: 🚫 Без VPN
    type: select
    proxies: [DIRECT]
rule-providers:
  ru:
    type: http
    behavior: domain
    url: https://example.com/ru.yaml
    path: ./ru.yaml
rules:
  - RULE-SET,ru,DIRECT
  - AND,((NETWORK,UDP),(DST-PORT,443)),REJECT
  - MATCH,🌍 Сеть
`
