package build

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"reflect"
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
