package build

import (
	"reflect"
	"testing"

	"mikan/internal/sub/model"
)

// orderNodes are two nodes of one subscription: their servers get the nodes' flags.
var orderNodes = []model.Node{
	{ID: 1, Name: "🇳🇱 Нидерланды", Endpoint: model.Endpoint{Host: "nl.example.com", SNI: "nl.example.com"}},
	{ID: 2, Name: "🇩🇪 Германия", Endpoint: model.Endpoint{Host: "de.example.com", SNI: "de.example.com"}},
}

// served is what the order of a subscription is told by: a proxy's name, node and host.
type served struct {
	Name       string
	Node, Host int64
}

func servedOf(ps []model.Proxy) []served {
	out := make([]served, len(ps))
	for i, p := range ps {
		out[i] = served{p.Name, p.Node, p.Host}
	}
	return out
}

// The servers go to the places of their hosts, across the nodes and the inbounds: a host of
// one node between two of another, an inbound's other host before its default one. A name is
// numbered in that order, so the first of two of a name is the one without a number.
func TestServersFollowTheirPlaces(t *testing.T) {
	ins := []model.Inbound{
		{ID: 1, Node: 1, Name: "TUIC", Port: 443, PortSpec: "443", Config: tuicBehindCDN(), Hosts: []model.Host{
			{ID: 11, Order: 4},
			{ID: 12, Order: 1, Address: "cdn.example.com"},
		}},
		{ID: 2, Node: 1, Name: "Fast", Named: true, Port: 8443, PortSpec: "8443", Config: tuicBehindCDN(), Hosts: []model.Host{{ID: 13, Order: 3}}},
		{ID: 3, Node: 2, Name: "TUIC", Port: 443, PortSpec: "443", Config: tuicBehindCDN(), Hosts: []model.Host{{ID: 21, Order: 2}}},
	}
	got := servedOf(Proxies(Profile{Keys: testKeys, Inbounds: ins, Nodes: orderNodes}))
	want := []served{
		{"🇳🇱 TUIC", 1, 12},
		{"🇩🇪 TUIC", 2, 21},
		{"Fast", 1, 13},
		{"🇳🇱 TUIC 2", 1, 11},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("servers:\n%+v\nwant\n%+v", got, want)
	}
	// Each proxy is whole, though two servers of one inbound are apart and the default one
	// renders second: the other host is whole of itself (its port empty: the inbound's), the
	// default one still reads the template's mikan.client.
	ps := Proxies(Profile{Keys: testKeys, Inbounds: ins, Nodes: orderNodes})
	if ps[0].Server != "cdn.example.com" || ps[0].Port != 443 || ps[3].Server != "cdn.example.com" || ps[3].Port != 8443 {
		t.Fatalf("servers of one inbound: %+v / %+v", ps[0], ps[3])
	}
}

// Without places (a profile of no panel, or every place the same) the servers come node by
// node in the nodes' order, in a node the inbounds' own, in an inbound the hosts'. A host
// without a place among placed ones keeps the place of the one before it.
func TestServersWithoutPlacesComeAsTheyAre(t *testing.T) {
	ins := []model.Inbound{
		{ID: 3, Node: 2, Name: "TUIC", Port: 443, PortSpec: "443", Config: tuicBehindCDN()},
		{ID: 1, Node: 1, Name: "TUIC", Port: 443, PortSpec: "443", Config: tuicBehindCDN(), Hosts: []model.Host{{ID: 11}, {ID: 12}}},
	}
	got := servedOf(Proxies(Profile{Keys: testKeys, Inbounds: ins, Nodes: orderNodes}))
	want := []served{{"🇳🇱 TUIC", 1, 11}, {"🇳🇱 TUIC 2", 1, 12}, {"🇩🇪 TUIC", 2, 0}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("without places:\n%+v\nwant\n%+v", got, want)
	}

	ins[0].Hosts = []model.Host{{ID: 21, Order: 1}}
	ins[1].Hosts = []model.Host{{ID: 11, Order: 2}, {ID: 12}}
	got = servedOf(Proxies(Profile{Keys: testKeys, Inbounds: ins, Nodes: orderNodes}))
	want = []served{{"🇩🇪 TUIC", 2, 21}, {"🇳🇱 TUIC", 1, 11}, {"🇳🇱 TUIC 2", 1, 12}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("a host without a place:\n%+v\nwant\n%+v", got, want)
	}
}

// A hidden host leaves its place empty; the others keep theirs.
func TestHiddenHostLeavesItsPlace(t *testing.T) {
	ins := []model.Inbound{
		{ID: 1, Node: 1, Name: "TUIC", Port: 443, PortSpec: "443", Config: tuicBehindCDN(), Hosts: []model.Host{
			{ID: 11, Order: 3, Hidden: true},
			{ID: 12, Order: 1},
		}},
		{ID: 3, Node: 2, Name: "TUIC", Port: 443, PortSpec: "443", Config: tuicBehindCDN(), Hosts: []model.Host{{ID: 21, Order: 2}}},
	}
	got := servedOf(Proxies(Profile{Keys: testKeys, Inbounds: ins, Nodes: orderNodes}))
	want := []served{{"🇳🇱 TUIC", 1, 12}, {"🇩🇪 TUIC", 2, 21}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("hidden host:\n%+v\nwant\n%+v", got, want)
	}
}
