package build

import (
	"testing"

	"mikan/internal/sub/model"
)

// A group's address goes over the default host alone: each field it sets, the rest as the
// template gives it; the inbound's other hosts stay where they are. Its name is the proxy's,
// so another host of the inbound takes the inbound's name, without a number.
func TestGroupAddressOverTheDefaultHost(t *testing.T) {
	in := model.Inbound{ID: 1, Node: 1, Name: "TUIC", Port: 443, PortSpec: "443", Config: tuicBehindCDN(), Hosts: []model.Host{
		{Address: "cdn.example.com", Port: 8443, SNI: "cdn.example.com", Fingerprint: "firefox"},
		{Address: "direct.example.com", Port: 2053, SNI: "direct.example.com"},
	}}
	proxies := func() []model.Proxy {
		return Proxies(Profile{Keys: testKeys, Inbounds: []model.Inbound{in}, Nodes: []model.Node{hostsNode}})
	}
	bare := proxies()
	in.Override = model.AddressOverride{Address: "edge.example.com", Port: 9443, Name: "Edge"}
	got := proxies()
	if len(got) != 2 || got[0].Name != "Edge" || got[0].Server != "edge.example.com" || got[0].Port != 9443 || got[0].TLS.SNI != "cdn.example.com" {
		t.Fatalf("the default host: %+v", got)
	}
	if got[1].Name != "TUIC" || got[1].Server != bare[1].Server || got[1].Port != bare[1].Port || got[1].TLS.SNI != bare[1].TLS.SNI {
		t.Fatalf("another host: %+v, was %+v", got[1], bare[1])
	}
	in.Override = model.AddressOverride{SNI: "edge.example.com"}
	if got := proxies(); got[0].TLS.SNI != "edge.example.com" || got[0].Server != "cdn.example.com" || got[0].Name != bare[0].Name {
		t.Fatalf("the TLS name alone: %+v", got[0])
	}
	// A hidden default host stays out, the group's address too.
	in.Hosts[0].Hidden, in.Override = true, model.AddressOverride{Address: "edge.example.com"}
	if got := proxies(); len(got) != 1 || got[0].Server != "direct.example.com" {
		t.Fatalf("a hidden default host: %+v", got)
	}
}
