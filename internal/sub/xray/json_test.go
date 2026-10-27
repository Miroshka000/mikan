package xray

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"mikan/internal/sub/mihomo"
	"mikan/internal/sub/model"
	"mikan/internal/sub/routing"
	"mikan/internal/sub/subtest"
	"mikan/internal/sub/subtest/corecheck"
)

// xrayConfig is the part of an xray JSON config the tests read.
type xrayConfig struct {
	Remarks string `json:"remarks"`
	DNS     struct {
		Tag     string `json:"tag"`
		Servers []any  `json:"servers"`
	} `json:"dns"`
	Inbounds  []map[string]any `json:"inbounds"`
	Outbounds []map[string]any `json:"outbounds"`
	Routing   struct {
		DomainStrategy string           `json:"domainStrategy"`
		Rules          []map[string]any `json:"rules"`
		Balancers      []Balancer       `json:"balancers"`
	} `json:"routing"`
	Observatory *Observatory `json:"observatory"`
}

func profileOf(u subtest.User) mihomo.Profile {
	return mihomo.Profile{Proxies: u.Proxies(), Nodes: u.Build.Nodes, Direct: u.Direct, Rules: u.Rules, Routes: u.Routes, Lang: u.Lang}
}

// render is the xray JSON profile of p, each config checked by xray.
func render(t *testing.T, p mihomo.Profile, m routing.Mode, tpl model.Template) []xrayConfig {
	t.Helper()
	raw, err := Render(p, model.GroupNames{}, m, tpl)
	if err != nil {
		t.Fatal(err)
	}
	var out []xrayConfig
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	t.Run("xray", func(t *testing.T) { corecheck.Xray(t, raw) })
	return out
}

func (c xrayConfig) outbound(t *testing.T, tag string) map[string]any {
	t.Helper()
	for _, o := range c.Outbounds {
		if o["tag"] == tag {
			return o
		}
	}
	t.Fatalf("%s: no outbound %s in %v", c.Remarks, tag, c.Outbounds)
	return nil
}

// last is where the config's last rule, which takes what the others leave, sends.
func (c xrayConfig) last() string {
	r := c.Routing.Rules[len(c.Routing.Rules)-1]
	if b, ok := r["balancerTag"].(string); ok {
		return "balancer:" + b
	}
	return r["outboundTag"].(string)
}

func get(m map[string]any, path ...string) any {
	var v any = m
	for _, k := range path {
		x, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = x[k]
	}
	return v
}

const pin = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// x25519 is a public key of X25519, as REALITY and VLESS Encryption take one.
const x25519 = "B6N8vBQgk8i3VdwbEOhstCY3StFqqFPtC9_AsrhtHHw"

// Every protocol xray carries as its outbound, the way the share links carry it; the rest
// is left out, with no mux on any.
func TestJSONOutbounds(t *testing.T) {
	reality := &model.TLS{SNI: "www.example.com", Fingerprint: "chrome", Reality: &model.Reality{PublicKey: x25519, ShortID: "a1b2"}}
	node := &model.TLS{SNI: "node.example.com", Fingerprint: "firefox", Pin: pin}
	xhttp := &model.XHTTP{Mode: "packet-up", Settings: []model.Setting{{Mihomo: "x-padding-bytes", Xray: "xPaddingBytes", Value: "100-1000"}},
		Reuse: []model.Setting{{Mihomo: "max-concurrency", Xray: "maxConcurrency", Value: "16-32"}}}
	ps := []model.Proxy{
		{Name: "vision", Type: "vless", Server: "203.0.113.7", Port: 8443, UUID: subtest.Keys.UUID, Flow: "xtls-rprx-vision", TLS: reality, Transport: &model.Transport{Network: "tcp"}},
		{Name: "ws", Type: "vless", Server: "203.0.113.7", Port: 2096, UUID: subtest.Keys.UUID, Encryption: "mlkem768x25519plus.native.0rtt." + x25519, TLS: node, Transport: &model.Transport{Network: "ws", Path: "/w", Host: "cdn.example.com"}},
		{Name: "grpc", Type: "vless", Server: "203.0.113.7", Port: 2053, UUID: subtest.Keys.UUID, TLS: reality, Transport: &model.Transport{Network: "grpc", Service: "svc"}},
		{Name: "xhttp", Type: "vless", Server: "203.0.113.7", Port: 443, UUID: subtest.Keys.UUID, TLS: reality, Transport: &model.Transport{Network: "xhttp", Path: "/x", Host: "h.example.com", XHTTP: xhttp}},
		{Name: "trojan", Type: "trojan", Server: "203.0.113.7", Port: 2087, Password: "pw", TLS: node, Transport: &model.Transport{Network: "tcp"}},
		{Name: "ss", Type: "shadowsocks", Server: "203.0.113.7", Port: 8388, Cipher: "2022-blake3-aes-128-gcm", Password: "AAAAAAAAAAAAAAAAAAAAAA==", UDPOverTCP: 2},
		{Name: "hy2", Type: "hysteria2", Server: "203.0.113.7", Port: 443, Ports: "20000-30000", Password: "pw", ALPN: []string{"h3"},
			TLS: &model.TLS{Pin: pin}, Obfs: &model.Obfs{Type: "salamander", Password: "obfs"}},
		{Name: "tuic", Type: "tuic", Server: "203.0.113.7", Port: 8443, UUID: subtest.Keys.UUID, Password: "pw", TLS: &model.TLS{}},
		{Name: "anytls", Type: "anytls", Server: "203.0.113.7", Port: 2083, Password: "pw", TLS: node},
		{Name: "gecko", Type: "hysteria2", Server: "203.0.113.7", Port: 443, Password: "pw", TLS: &model.TLS{}, Obfs: &model.Obfs{Type: "gecko"}},
		{Name: "bad pin", Type: "trojan", Server: "203.0.113.7", Port: 2087, Password: "pw", TLS: &model.TLS{Pin: "ab12"}, Transport: &model.Transport{Network: "tcp"}},
	}
	cs := render(t, mihomo.Profile{Proxies: ps}, routing.All, model.Template{})
	var servers []string
	for _, c := range cs[1:] {
		servers = append(servers, c.Remarks)
	}
	if cs[0].Remarks != "Авто" || !slices.Equal(servers, []string{"vision", "ws", "grpc", "xhttp", "trojan", "ss", "hy2"}) {
		t.Fatalf("configs: %q then %q", cs[0].Remarks, servers)
	}
	raw, _ := json.Marshal(cs)
	if strings.Contains(string(raw), `"mux"`) || strings.Contains(string(raw), "allowInsecure") {
		t.Fatal("no mux, and no allowInsecure, which xray refuses")
	}
	of := func(name string) map[string]any {
		for _, c := range cs {
			if c.Remarks == name {
				return c.Outbounds[0]
			}
		}
		t.Fatalf("no config %s", name)
		return nil
	}
	vision := of("vision")
	user := get(vision, "settings")
	if u := user.(map[string]any)["vnext"].([]any)[0].(map[string]any)["users"].([]any)[0].(map[string]any); u["flow"] != "xtls-rprx-vision" || u["encryption"] != "none" {
		t.Fatalf("vision user: %v", u)
	}
	if get(vision, "streamSettings", "security") != "reality" || get(vision, "streamSettings", "realitySettings", "publicKey") != x25519 ||
		get(vision, "streamSettings", "realitySettings", "fingerprint") != "chrome" {
		t.Fatalf("vision: %v", vision)
	}
	ws := of("ws")
	if u := get(ws, "settings").(map[string]any)["vnext"].([]any)[0].(map[string]any)["users"].([]any)[0].(map[string]any); u["encryption"] != "mlkem768x25519plus.native.0rtt."+x25519 {
		t.Fatalf("VLESS Encryption: %v", u)
	}
	if get(ws, "streamSettings", "wsSettings", "host") != "cdn.example.com" || get(ws, "streamSettings", "tlsSettings", "pinnedPeerCertSha256") != pin ||
		get(ws, "streamSettings", "tlsSettings", "serverName") != "node.example.com" || get(ws, "streamSettings", "tlsSettings", "fingerprint") != "firefox" {
		t.Fatalf("ws: %v", ws)
	}
	if g := get(of("grpc"), "streamSettings", "grpcSettings").(map[string]any); g["serviceName"] != "svc" || g["idle_timeout"] != float64(15) || g["health_check_timeout"] != float64(10) {
		t.Fatalf("grpc keepalive: %v", g)
	}
	x := get(of("xhttp"), "streamSettings", "xhttpSettings").(map[string]any)
	extra, _ := json.Marshal(x["extra"])
	if x["path"] != "/x" || x["host"] != "h.example.com" || x["mode"] != "packet-up" || string(extra) != `{"xPaddingBytes":"100-1000","xmux":{"maxConcurrency":"16-32"}}` {
		t.Fatalf("xhttp: %v", x)
	}
	if !strings.Contains(Link(ps[3]), "extra=%7B%22xPaddingBytes%22%3A%22100-1000%22%2C%22xmux%22%3A%7B%22maxConcurrency%22%3A%2216-32%22%7D%7D") {
		t.Fatalf("the link carries the same extra: %s", Link(ps[3]))
	}
	if s := get(of("trojan"), "settings").(map[string]any)["servers"].([]any)[0].(map[string]any); s["password"] != "pw" || get(of("trojan"), "streamSettings", "security") != "tls" {
		t.Fatalf("trojan: %v", of("trojan"))
	}
	if s := get(of("ss"), "settings").(map[string]any)["servers"].([]any)[0].(map[string]any); s["method"] != "2022-blake3-aes-128-gcm" || s["uot"] != true || s["UoTVersion"] != float64(2) {
		t.Fatalf("shadowsocks: %v", s)
	}
	hy2 := of("hy2")
	masks, _ := json.Marshal(get(hy2, "streamSettings", "finalmask"))
	if hy2["protocol"] != "hysteria" || get(hy2, "settings", "version") != float64(2) || get(hy2, "streamSettings", "hysteriaSettings", "auth") != "pw" ||
		get(hy2, "streamSettings", "tlsSettings", "pinnedPeerCertSha256") != pin ||
		string(masks) != `{"udp":[{"settings":{"password":"obfs"},"type":"salamander"}]}` {
		t.Fatalf("hysteria2 (no port hopping: older cores refuse its mask): %v, masks %s", hy2, masks)
	}
	if _, err := Render(mihomo.Profile{Proxies: ps[7:]}, model.GroupNames{}, routing.All, model.Template{}); !errors.Is(err, model.ErrNoProxies) {
		t.Fatalf("nothing xray carries: %v", err)
	}
}

// The list of configs: the automatic choice and a config per country, each a balancer xray
// tests the servers with, then a config per server; each sends the main group to its own
// choice and carries no more servers than it needs.
func TestJSONConfigs(t *testing.T) {
	cs := render(t, profileOf(subtest.TwoNodes()), routing.All, model.Template{})
	var names []string
	for _, c := range cs {
		names = append(names, c.Remarks)
	}
	want := []string{"Авто", "🇳🇱 Нидерланды", "🇺🇸 США", "🇳🇱 VLESS XHTTP", "🇳🇱 Hysteria2", "🇳🇱 VLESS Vision", "🇺🇸 VLESS XHTTP"}
	if !slices.Equal(names, want) {
		t.Fatalf("configs: %q", names)
	}
	auto := cs[0]
	if auto.last() != "balancer:Авто" || len(auto.Routing.Balancers) != 1 || auto.Observatory == nil ||
		!slices.Equal(auto.Observatory.SubjectSelector, auto.Routing.Balancers[0].Selector) || len(auto.Routing.Balancers[0].Selector) != 4 {
		t.Fatalf("auto: %+v", auto)
	}
	us := cs[2]
	if us.last() != "balancer:🇺🇸 США" || !slices.Equal(us.Routing.Balancers[0].Selector, []string{"proxy-4"}) || len(us.Outbounds) != 4 {
		t.Fatalf("a country holds its servers alone: %+v", us)
	}
	server := cs[4]
	if server.last() != "proxy-2" || server.Outbounds[0]["tag"] != "proxy-2" || server.Outbounds[0]["protocol"] != "hysteria" ||
		len(server.Outbounds) != 4 || len(server.Routing.Balancers) != 0 || server.Observatory != nil {
		t.Fatalf("a server's config: %+v", server)
	}
	for _, c := range cs {
		var tags []string
		for _, o := range c.Outbounds {
			tags = append(tags, o["tag"].(string))
		}
		for _, a := range tags {
			for _, b := range tags {
				if a != b && strings.HasPrefix(b, a) {
					t.Fatalf("%s: tag %s begins with %s: a balancer would select both", c.Remarks, b, a)
				}
			}
		}
		in := c.Inbounds
		if len(in) != 2 || in[0]["port"] != float64(10808) || in[0]["listen"] != "127.0.0.1" || in[1]["port"] != float64(10809) || get(in[1], "sniffing", "enabled") != true {
			t.Fatalf("inbounds: %v", in)
		}
		if c.DNS.Tag != "dns" || c.Routing.Rules[0]["inboundTag"] == nil {
			t.Fatalf("%s: xray's own queries are routed first: %v", c.Remarks, c.Routing.Rules[0])
		}
	}
}

// The panel's routing as xray matches it: the hosts, the LAN and QUIC, the admin's rules,
// the services by runetfreedom's categories and a service to one node, the mode's own rules,
// and the DNS through the tunnel with the Russian names past it.
func TestJSONRouting(t *testing.T) {
	u := subtest.TwoNodes()
	u.Direct = []string{"203.0.113.7", "vpn.example.com"}
	u.Rules = subtest.Rules
	u.Routes = subtest.Routes()
	cs := render(t, profileOf(u), routing.RUDirect, model.Template{})
	nl := cs[3] // the first server's
	if nl.Remarks != "🇳🇱 VLESS XHTTP" || nl.Routing.DomainStrategy != "IPIfNonMatch" {
		t.Fatalf("config %q, %s", nl.Remarks, nl.Routing.DomainStrategy)
	}
	var got []string
	for _, r := range nl.Routing.Rules {
		delete(r, "type")
		b, _ := json.Marshal(r)
		got = append(got, string(b))
	}
	for _, w := range []string{
		`{"domain":["full:dns.alidns.com"],"inboundTag":["dns"],"outboundTag":"direct"}`,
		`{"inboundTag":["dns"],"ip":["223.5.5.5"],"outboundTag":"direct"}`,
		`{"outboundTag":"dns-out","port":"53"}`,
		`{"ip":["203.0.113.7/32"],"outboundTag":"direct"}`,
		`{"domain":["full:vpn.example.com"],"outboundTag":"direct"}`,
		`{"ip":["geoip:private"],"outboundTag":"direct"}`,
		`{"network":"udp","outboundTag":"block","port":"443"}`,
		`{"domain":["domain:sber.ru"],"outboundTag":"direct"}`,
		`{"domain":["regexp:^.*\\.tinkoff\\.ru$"],"outboundTag":"direct"}`,
		`{"ip":["10.0.0.0/8"],"outboundTag":"direct"}`,
		`{"domain":["geosite:category-ads-all"],"outboundTag":"block"}`,
		`{"domain":["geosite:youtube"],"outboundTag":"proxy-1"}`,
		`{"domain":["geosite:category-ai-!cn"],"outboundTag":"proxy-4"}`,
		`{"domain":["geosite:category-public-tracker"],"outboundTag":"direct"}`,
		`{"domain":["geosite:category-ru"],"outboundTag":"direct"}`,
		`{"ip":["geoip:ru"],"outboundTag":"direct"}`,
		`{"network":"tcp,udp","outboundTag":"proxy-1"}`,
	} {
		if !slices.Contains(got, w) {
			t.Errorf("no rule %s in\n%s", w, strings.Join(got, "\n"))
		}
	}
	// The admin's own list is no category xray has: it is left out, and so is its rule.
	if raw, _ := json.Marshal(nl); strings.Contains(string(raw), "wl.yaml") || strings.Contains(string(raw), "PROCESS") {
		t.Fatal("a list xray cannot read")
	}
	if got[len(got)-1] != `{"network":"tcp,udp","outboundTag":"proxy-1"}` {
		t.Fatalf("the last rule takes the rest: %s", got[len(got)-1])
	}
	// A condition of a rule before (Telegram's IPs as a category and as a list) is never
	// reached, and is left out.
	n := 0
	for _, r := range got {
		if r == `{"ip":["geoip:telegram"],"outboundTag":"proxy-1"}` {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("a rule %d times:\n%s", n, strings.Join(got, "\n"))
	}
	// The service on the US node goes to its server, which the config carries for it.
	nl.outbound(t, "proxy-4")
	servers, _ := json.Marshal(nl.DNS.Servers)
	if string(servers) != `[{"address":"223.5.5.5","domains":["domain:cn"],"skipFallback":true},"https://dns.alidns.com/dns-query"]` {
		t.Fatalf("the admin's DNS: %s", servers)
	}
	// The panel's own resolvers: through the tunnel, the Russian names past it.
	cs = render(t, profileOf(subtest.TwoNodes()), routing.RUDirect, model.Template{})
	servers, _ = json.Marshal(cs[0].DNS.Servers)
	if string(servers) != `[{"address":"77.88.8.8","domains":["geosite:category-ru"],"skipFallback":true},{"address":"77.88.8.1","domains":["geosite:category-ru"],"skipFallback":true},"https://1.1.1.1/dns-query","https://8.8.8.8/dns-query"]` {
		t.Fatalf("the panel's DNS: %s", servers)
	}
	if r, _ := json.Marshal(cs[0].Routing.Rules[1]); string(r) != `{"balancerTag":"Авто","inboundTag":["dns"],"ip":["1.1.1.1","8.8.8.8"]}` {
		t.Fatalf("the DNS goes through the config's choice: %s", r)
	}
	// Only the rules that match an IP the app resolves a name for make it resolve.
	if cs := render(t, profileOf(subtest.TwoNodes()), routing.All, model.Template{}); cs[0].Routing.DomainStrategy != "AsIs" {
		t.Fatalf("all through the tunnel: %s", cs[0].Routing.DomainStrategy)
	}
}

// An own Clash profile: its automatic group is the first config, a select of one country is
// a server and gets none of its own; its rules and DNS go as xray matches them.
func TestJSONTemplate(t *testing.T) {
	cs := render(t, profileOf(subtest.TwoNodes()), routing.All, subtest.Clash(subtest.OwnTemplate))
	if cs[0].Remarks != "⚡ Авто" || cs[1].Remarks != "🇳🇱 VLESS XHTTP" || len(cs) != 5 {
		t.Fatalf("configs: %q %q of %d", cs[0].Remarks, cs[1].Remarks, len(cs))
	}
	// The automatic group leaves Hysteria2 out, as the profile says.
	if b := cs[0].Routing.Balancers; len(b) != 1 || slices.Contains(b[0].Selector, "proxy-2") || len(b[0].Selector) != 3 {
		t.Fatalf("balancer: %+v", b)
	}
	got, _ := json.Marshal(cs[1].Routing.Rules[len(cs[1].Routing.Rules)-2:])
	if string(got) != `[{"network":"udp","outboundTag":"block","port":"443"},{"network":"tcp,udp","outboundTag":"proxy-1"}]` {
		t.Fatalf("rules: %s", got)
	}
}

// A select group that starts with the main group follows the config's choice: the PROXY
// alias and a service sent to it ([PROXY, DIRECT]) go where the config's main group goes,
// not to the main group's first server.
func TestJSONTemplateSelectFollowsTheChoice(t *testing.T) {
	src := `mode: rule
proxy-groups:
  - {name: Сеть, type: select, proxies: [Авто, DIRECT]}
  - {name: Авто, type: url-test, include-all-proxies: true}
  - {name: PROXY, type: select, proxies: [Сеть]}
  - {name: Telegram, type: select, proxies: [PROXY, DIRECT]}
rules:
  - DOMAIN-SUFFIX,t.me,Telegram
  - MATCH,Сеть
`
	cs := render(t, profileOf(subtest.TwoNodes()), routing.All, subtest.Clash(src))
	if len(cs) < 2 {
		t.Fatalf("configs: %d", len(cs))
	}
	for _, c := range cs {
		var rules []map[string]any
		b, _ := json.Marshal(c.Routing.Rules)
		if err := json.Unmarshal(b, &rules); err != nil || len(rules) == 0 {
			t.Fatalf("%s: rules %s", c.Remarks, b)
		}
		var tg map[string]any
		for _, r := range rules {
			if d, _ := json.Marshal(r["domain"]); strings.Contains(string(d), "t.me") {
				tg = r
			}
		}
		last := rules[len(rules)-1]
		if tg == nil || tg["outboundTag"] != last["outboundTag"] || tg["balancerTag"] != last["balancerTag"] {
			t.Fatalf("%s: telegram %v, the rest %v", c.Remarks, tg, last)
		}
	}
}

// The preview's notes are of the groups xray turns into another form; the main group is the
// app's list of configs and has none.
func TestJSONNotes(t *testing.T) {
	p := profileOf(subtest.TwoNodes())
	if n := Notes(p, model.GroupNames{}, routing.All, model.Template{}); len(n) != 0 {
		t.Fatalf("built-in: %+v", n)
	}
	p.Routes.Servers = routing.Servers{Auto: "fallback"}
	if n := Notes(p, model.GroupNames{}, routing.All, model.Template{}); len(n) != 1 || n[0].Group != "Авто" || n[0].Code != "fallback_fastest" {
		t.Fatalf("fallback: %+v", n)
	}
}

// An app runs the configs with the geo files it has: they name only what every usual
// geodata has, and the blocked mode, made of runetfreedom's lists of what is blocked, turns
// into RU direct (an app would fail on ru-blocked otherwise).
func TestXrayJSONCommonGeodata(t *testing.T) {
	v := subtest.Variants[slices.IndexFunc(subtest.Variants, func(v subtest.Variant) bool { return v.Name == "multi-custom" })]
	for _, m := range subtest.Modes {
		raw, err := Render(profileOf(v.User()), v.Groups, m, model.Template{})
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range []string{"ru-available-only-inside", "ru-blocked"} {
			if strings.Contains(string(raw), c) {
				t.Errorf("%s: %s, which Loyalsoldier's geodata lacks", m, c)
			}
		}
		if m != routing.All && !strings.Contains(string(raw), `"geoip:ru"`) {
			t.Errorf("%s: the Russian IPs go direct", m)
		}
		t.Run(string(m), func(t *testing.T) { corecheck.Xray(t, raw) })
	}
}

// A node kept out of the automatic choice is not in the "Auto" balancer: its selector lists
// the exact tags of the other node's servers.
func TestXrayAutoLeavesNodesOut(t *testing.T) {
	u := subtest.TwoNodes()
	u.Build.Nodes[1].NoAuto = true
	cs := render(t, profileOf(u), routing.All, model.Template{})
	auto := cs[0]
	if auto.Remarks != "Авто" || len(auto.Routing.Balancers) == 0 {
		t.Fatalf("the first config: %q %+v", auto.Remarks, auto.Routing.Balancers)
	}
	sel := auto.Routing.Balancers[0].Selector
	if len(sel) == 0 {
		t.Fatal("an empty choice")
	}
	for _, tag := range sel {
		if out, _ := json.Marshal(auto.outbound(t, tag)); strings.Contains(string(out), "198.51.100.9") {
			t.Fatalf("the US node's server %s is in the choice: %s", tag, out)
		}
	}
}

// An own Clash profile with a chain: the entry, then the exits a url-test picks of (an XHTTP
// and a Hysteria2 hop), with a fallback and a load-balance beside it, all that no rule takes
// sent to the chain. An exit's config dials it through the entry, which the config carries
// for that alone (no balancer selects it).
func TestJSONTemplateChain(t *testing.T) {
	src := `mode: rule
proxy-groups:
  - {name: Каскад, type: chain, proxies: [🇳🇱 VLESS Vision, Выходы]}
  - {name: Выходы, type: url-test, proxies: [🇺🇸 VLESS XHTTP, 🇳🇱 Hysteria2]}
  - {name: Запас, type: fallback, proxies: [🇳🇱 VLESS XHTTP, 🇺🇸 VLESS XHTTP]}
  - {name: Баланс, type: load-balance, strategy: round-robin, proxies: [🇳🇱 VLESS XHTTP, 🇳🇱 Hysteria2]}
rules:
  - DOMAIN-SUFFIX,example.org,Запас
  - DOMAIN-SUFFIX,example.net,Баланс
  - MATCH,Каскад
`
	p, tpl := profileOf(subtest.TwoNodes()), subtest.Clash(src)
	cs := render(t, p, routing.All, tpl)
	var names []string
	for _, c := range cs {
		names = append(names, c.Remarks)
	}
	// The chain goes out of its exits' balancer, no group's own: a config per server.
	if !slices.Equal(names, []string{"🇳🇱 VLESS XHTTP", "🇳🇱 Hysteria2", "🇳🇱 VLESS Vision", "🇺🇸 VLESS XHTTP"}) {
		t.Fatalf("configs: %q", names)
	}
	const entry = "proxy-3" // 🇳🇱 VLESS Vision
	for _, c := range cs {
		byTag := map[string]Balancer{}
		for _, x := range c.Routing.Balancers {
			byTag[x.Tag] = x
		}
		if f := byTag["Запас"]; f.Strategy.Type != "leastPing" || f.FallbackTag != "proxy-1" || !slices.Equal(f.Selector, []string{"proxy-1", "proxy-4"}) {
			t.Errorf("%s: fallback %+v", c.Remarks, f)
		}
		if l := byTag["Баланс"]; l.Strategy.Type != "roundRobin" || !slices.Equal(l.Selector, []string{"proxy-1", "proxy-2"}) {
			t.Errorf("%s: load-balance %+v", c.Remarks, l)
		}
		// Each config sends somewhere an exit, and so carries the entry.
		if get(c.outbound(t, entry), "streamSettings", "sockopt") != nil {
			t.Errorf("%s: the entry dials through nothing: %v", c.Remarks, c.outbound(t, entry))
		}
	}
	for i, exit := range map[int]string{1: "proxy-2", 3: "proxy-4"} {
		c := cs[i]
		if c.last() != exit || c.Outbounds[0]["tag"] != exit || get(c.Outbounds[0], "streamSettings", "sockopt", "dialerProxy") != entry {
			t.Errorf("%s: the exit %s dials through the entry: %v", c.Remarks, exit, c.Outbounds[0])
		}
	}
	// dialed gives the exit a copy: the proxy's own outbound, which every config shares, keeps
	// its streamSettings.
	pl, err := planOf(p, model.GroupNames{}, routing.All, tpl)
	if err != nil {
		t.Fatal(err)
	}
	b := pl.balancing()
	if b.Dialers["proxy-2"] != entry || b.Dialers["proxy-4"] != entry || len(b.Dialers) != 2 {
		t.Fatalf("dialers: %v", b.Dialers)
	}
	for _, o := range pl.outbounds {
		before, _ := json.Marshal(o.out)
		d := dialed(o, b)
		after, _ := json.Marshal(o.out)
		if string(before) != string(after) || get(o.out, "streamSettings", "sockopt") != nil {
			t.Errorf("%s: dialed changed the outbound:\n%s\n%s", o.tag, before, after)
		}
		if got, want := get(d, "streamSettings", "sockopt", "dialerProxy"), b.Dialers[o.tag]; want != "" && got != want || want == "" && got != nil {
			t.Errorf("%s dials through %v, want %q", o.tag, got, want)
		}
	}
}
