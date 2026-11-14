package build

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"mikan/internal/proto"
	"mikan/internal/sub/model"
)

// hostsNode is a node with a certificate of its own, so a protocol on it has a TLS name.
var hostsNode = model.Node{ID: 1, Name: "🇳🇱 Нидерланды", Endpoint: model.Endpoint{Host: "nl.example.com", SNI: "nl.example.com"}}

// tuicBehindCDN is a TUIC template whose mikan.client sends the apps through a CDN.
func tuicBehindCDN() string {
	return proto.Marshal(proto.Template{"type": "tuic", "alpn": []any{"h3"},
		"mikan": map[string]any{"client": map[string]any{"server": "cdn.example.com", "port": 8443, "sni": "cdn.example.com", "fingerprint": "firefox"}}})
}

// The default host is the template's mikan.client: an inbound that has only it, or no
// hosts at all, gives the very proxy it gave before it had hosts.
func TestDefaultHostIsTheTemplate(t *testing.T) {
	in := model.Inbound{ID: 1, Node: 1, Name: "TUIC", Port: 443, PortSpec: "443", Config: tuicBehindCDN()}
	bare := Proxies(Profile{Keys: testKeys, Inbounds: []model.Inbound{in}, Nodes: []model.Node{hostsNode}})
	if len(bare) != 1 || bare[0].Server != "cdn.example.com" || bare[0].Port != 8443 || bare[0].TLS.SNI != "cdn.example.com" {
		t.Fatalf("without hosts: %+v", bare)
	}
	// A default host that drifted from the template (it never should) changes nothing.
	for _, hosts := range [][]model.Host{
		{{Address: "cdn.example.com", Port: 8443, SNI: "cdn.example.com", Fingerprint: "firefox"}},
		{{Address: "other.example.com"}},
	} {
		in.Hosts = hosts
		got := Proxies(Profile{Keys: testKeys, Inbounds: []model.Inbound{in}, Nodes: []model.Node{hostsNode}})
		if !reflect.DeepEqual(got, bare) {
			t.Fatalf("with hosts %+v:\n%+v\nwant\n%+v", hosts, got, bare)
		}
	}
}

// Every other host is one more proxy right after the default one, named as the inbound
// with a number. Its fields are whole: one left empty is what the apps get without any,
// the node's address and certificate name, the inbound's port, the panel's fingerprint,
// not the template's mikan.client.
func TestEveryHostIsAProxy(t *testing.T) {
	ins := []model.Inbound{
		{ID: 1, Node: 1, Name: "Fast", Named: true, Port: 443, PortSpec: "443", Config: tuicBehindCDN(), Hosts: []model.Host{
			{Address: "cdn.example.com", Port: 8443, SNI: "cdn.example.com", Fingerprint: "firefox"},
			{Address: "direct.example.com", Port: 2053, SNI: "direct.example.com", Fingerprint: "safari"},
			{},
		}},
		{ID: 2, Node: 1, Name: "Fast", Named: true, Port: 8443, PortSpec: "8443", Config: tuicBehindCDN()},
	}
	type seen struct {
		Name, Server string
		Port         int
		SNI, Print   string
	}
	var got []seen
	for _, p := range Proxies(Profile{Keys: testKeys, Inbounds: ins, Nodes: []model.Node{hostsNode}, Fingerprint: "edge"}) {
		got = append(got, seen{p.Name, p.Server, p.Port, p.TLS.SNI, p.TLS.Fingerprint})
	}
	want := []seen{
		{"Fast", "cdn.example.com", 8443, "cdn.example.com", ""},
		{"Fast 2", "direct.example.com", 2053, "direct.example.com", ""},
		{"Fast 3", "nl.example.com", 443, "nl.example.com", ""},
		{"Fast 4", "cdn.example.com", 8443, "cdn.example.com", ""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("proxies:\n%+v\nwant\n%+v", got, want)
	}
}

// A host of a REALITY inbound with no TLS name of its own keeps the inbound's first server
// name, and one with no fingerprint gets the panel's, not the template's mikan.client; a
// WebSocket host with no HTTP Host sends none, as the server's own.
func TestHostOverTLSFallsBack(t *testing.T) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	priv := base64.RawURLEncoding.EncodeToString(k.Bytes())
	reality := "type: vless\nreality-config: {dest: www.microsoft.com:443, private-key: " + priv +
		", short-id: [ab], server-names: [www.microsoft.com, www.bing.com]}\nmikan: {client: {sni: www.bing.com, fingerprint: ios}}\n"
	ws := "type: vless\nws-path: /w\nmikan: {tls: node, client: {host: cdn.example.com, fingerprint: ios}}\n"
	ins := []model.Inbound{
		{ID: 1, Node: 1, Name: "R", Named: true, Port: 443, PortSpec: "443", Config: reality, Hosts: []model.Host{{SNI: "www.bing.com", Fingerprint: "ios"}, {Address: "r.example.com"}}},
		{ID: 2, Node: 1, Name: "W", Named: true, Port: 443, PortSpec: "443", Config: ws, Hosts: []model.Host{{Host: "cdn.example.com", Fingerprint: "ios"}, {SNI: "w.example.com"}}},
	}
	type seen struct{ Name, Server, SNI, Print, HTTPHost string }
	var got []seen
	for _, p := range Proxies(Profile{Keys: testKeys, Inbounds: ins, Nodes: []model.Node{hostsNode}, Fingerprint: "edge"}) {
		s := seen{Name: p.Name, Server: p.Server, SNI: p.TLS.SNI, Print: p.TLS.Fingerprint}
		if p.Transport != nil {
			s.HTTPHost = p.Transport.Host
		}
		got = append(got, s)
	}
	want := []seen{
		{"R", "nl.example.com", "www.bing.com", "ios", ""},
		{"R 2", "r.example.com", "www.microsoft.com", "edge", ""},
		{"W", "nl.example.com", "nl.example.com", "ios", "cdn.example.com"},
		{"W 2", "nl.example.com", "w.example.com", "edge", ""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("proxies:\n%+v\nwant\n%+v", got, want)
	}
}

// A Hysteria2 host that sets a port, the range's first one too, gets that port alone: the
// proxy in front of the node may forward no other. One with no port keeps the hopping.
func TestHostPortStopsHopping(t *testing.T) {
	hy2 := proto.Marshal(proto.Template{"type": "hysteria2", "alpn": []any{"h3"}})
	in := model.Inbound{ID: 1, Node: 1, Name: "H", Named: true, Port: 20000, PortSpec: "20000-20099", Config: hy2,
		Hosts: []model.Host{{}, {Address: "relay.example.com", Port: 20000}, {Address: "relay.example.com", Port: 30000}, {Address: "other.example.com"}}}
	var got []string
	for _, p := range Proxies(Profile{Keys: testKeys, Inbounds: []model.Inbound{in}, Nodes: []model.Node{hostsNode}}) {
		got = append(got, p.Server+":"+strconv.Itoa(p.Port)+" "+p.Ports)
	}
	want := []string{"nl.example.com:20000 20000-20099", "relay.example.com:20000 ", "relay.example.com:30000 ", "other.example.com:20000 20000-20099"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("proxies:\n%q\nwant\n%q", got, want)
	}
}

// An address's own fields go on its proxy where the protocol carries them, and one the
// template no longer takes is left out rather than breaking the proxy. A CDN in front
// leaves the node's self-signed certificate unpinned.
func TestHostOptionsOnProxies(t *testing.T) {
	pinned := model.Node{ID: 1, Name: "n", Endpoint: model.Endpoint{Host: "198.51.100.1", PinSHA256: strings.Repeat("ab", 32)}}
	ws := "type: vless\nws-path: /w\nmikan: {tls: node}\n"
	grpc := "type: trojan\ngrpc-service-name: g\nmikan: {tls: node}\n"
	hy2 := proto.Marshal(proto.Template{"type": "hysteria2", "alpn": []any{"h3"}})
	vision := "type: vless\nmikan: {tls: node, flow: xtls-rprx-vision}\n"
	all := Options{ALPN: []string{"http/1.1", "h2", "h3"}, IPVersion: "ipv4", Mux: true, MuxProtocol: "yamux", MuxPadding: true}
	for _, c := range []struct {
		name, src string
		o         Options
		alpn      string
		mux       bool
		pin       bool
	}{
		{"ws", ws, all, "http/1.1,h2", true, true},
		{"ws without http/1.1", ws, Options{ALPN: []string{"h2"}}, "", false, true},
		{"grpc", grpc, all, "http/1.1,h2", true, true},
		{"hysteria2", hy2, all, "h3", false, true},
		{"vision", vision, all, "http/1.1,h2", false, true},
		{"ws behind a CDN", ws, Options{CDN: true}, "", false, false},
	} {
		tpl, err := proto.Parse(c.src)
		if err != nil {
			t.Fatal(err)
		}
		p, err := Proxy(tpl, Input{Name: "x", Host: pinned.Endpoint.Host, Port: 443, PortSpec: "443", PinSHA256: pinned.Endpoint.PinSHA256, Keys: testKeys, Options: c.o})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := strings.Join(p.ALPN, ","); got != c.alpn {
			t.Errorf("%s: ALPN %q, want %q", c.name, got, c.alpn)
		}
		if (p.Smux != nil) != c.mux || c.mux && (p.Smux.Protocol != "yamux" || !p.Smux.Padding) {
			t.Errorf("%s: mux %+v, want %v", c.name, p.Smux, c.mux)
		}
		if (p.TLS.Pin != "") != c.pin {
			t.Errorf("%s: pin %q, want %v", c.name, p.TLS.Pin, c.pin)
		}
		if p.IPVersion != c.o.IPVersion {
			t.Errorf("%s: IP version %q", c.name, p.IPVersion)
		}
	}
}

// A host's own name is used as typed, with the user's values in place of the variables; a
// default host's too. One that fills to no name a proxy can have, blank or a policy of
// mihomo's, falls back to the inbound's; the marks no name has become spaces.
func TestHostNames(t *testing.T) {
	in := model.Inbound{ID: 1, Node: 1, Name: "Fast", Port: 443, PortSpec: "443", Config: tuicBehindCDN(), Hosts: []model.Host{
		{Name: "🇳🇱 NL · {days} дн."},
		{Name: "NL · осталось {left}"},
		{Name: "{name}"},
		{Name: "{name}"},
		{Name: "{brand}"},
		{},
		{Name: "{unknown} x"},
	}}
	other := model.Node{ID: 2, Name: "🇩🇪 DE"}
	vars := map[string]string{"days": "12", "left": "3.5 ГБ", "name": "a,\"b\"", "brand": "direct"}
	var got []string
	for _, p := range Proxies(Profile{Keys: testKeys, Inbounds: []model.Inbound{in}, Nodes: []model.Node{hostsNode, other}, Vars: vars}) {
		got = append(got, p.Name)
	}
	want := []string{"🇳🇱 NL · 12 дн.", "NL · осталось 3.5 ГБ", "a  b", "a  b 2", "🇳🇱 Fast", "🇳🇱 Fast 2", "{unknown} x"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("names:\n%q\nwant\n%q", got, want)
	}
	// A user's {name} that is a group's: numbered, as the apps refuse a proxy named so.
	groups := model.GroupNames{}.WithDefaults("")
	two := model.Inbound{ID: 2, Node: 1, Name: "Fast", Named: true, Port: 443, PortSpec: "443", Config: tuicBehindCDN(),
		Hosts: []model.Host{{Name: "{name}"}, {Name: "{name}"}, {Name: "proxy"}}}
	got = nil
	for _, p := range Proxies(Profile{Keys: testKeys, Inbounds: []model.Inbound{two}, Nodes: []model.Node{hostsNode}, Vars: map[string]string{"name": "vpn"}, Groups: groups}) {
		got = append(got, p.Name)
	}
	if want := []string{"vpn 2", "vpn 3", "proxy 2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("names of groups:\n%q\nwant\n%q", got, want)
	}
	if got := HostNames([]model.Inbound{in}); len(got) != 6 {
		t.Fatalf("names with variables: %q", got)
	}
	long := fillName(strings.Repeat("я", 40)+"{name}", map[string]string{"name": strings.Repeat("ю", 40)})
	if n := len([]rune(long)); n != NameMax {
		t.Fatalf("a long name keeps %d letters", n)
	}
}
