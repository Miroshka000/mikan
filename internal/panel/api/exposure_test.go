package api

import (
	"slices"
	"testing"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/presets"
	"mikan/internal/panel/store/db"
	"mikan/internal/proto"
)

func exposureInbound(t *testing.T, name, preset, port, dest string) db.Inbound {
	t.Helper()
	cfg, err := presets.NewConfig(preset, dest)
	if err != nil {
		t.Fatalf("%s: %v", preset, err)
	}
	return db.Inbound{Name: name, Preset: preset, Port: port, Enabled: 1, Config: cfg}
}

func exposureCodes(items []CheckItem) []string {
	var out []string
	for _, it := range items {
		if it.ID != "exposure" {
			continue
		}
		if it.Status == nodeapi.CheckOK {
			out = append(out, "ok")
		} else {
			out = append(out, it.Code)
		}
	}
	slices.Sort(out)
	return out
}

func TestExposureOfAQuietNode(t *testing.T) {
	e := exposure{node: db.Node{Domain: "shop.example.com"}, names: []string{"my.example.com"}, inbounds: []db.Inbound{
		exposureInbound(t, "vless-xhttp", "vless_reality_xhttp", "443", "news.example.org:443"),
		exposureInbound(t, "vless-vision", "vless_reality_vision", "8443", "news.example.org:443"),
		exposureInbound(t, "hysteria2", "hysteria2", "2083", ""),
	}}
	if got := exposureCodes(e.items()); !slices.Equal(got, []string{"ok"}) {
		t.Fatalf("one site, two TLS ports, plain names: %v", got)
	}
}

// Three REALITY ports with a stranger's certificate each, one of them a dynamic-DNS name,
// one still on the site every installation starts with.
func TestExposureOfANodeThatStandsOut(t *testing.T) {
	off := exposureInbound(t, "old", "vless_reality_grpc", "9443", "gone.example.org:443")
	off.Enabled = 0
	e := exposure{inbounds: []db.Inbound{
		exposureInbound(t, "vless-xhttp", "vless_reality_xhttp", "9443", presets.DefaultDest),
		exposureInbound(t, "vless-vision", "vless_reality_vision", "8443", "home.duckdns.org:443"),
		exposureInbound(t, "vless-grpc", "vless_reality_grpc", "2053", "news.example.org:443"),
		off,
	}}
	items := e.items()
	if got := exposureCodes(items); !slices.Equal(got, []string{"default_site", "many_ports", "many_sites", "weak_site"}) {
		t.Fatalf("codes: %v", got)
	}
	for _, it := range items {
		switch it.Code {
		case "many_sites":
			if it.Params["n"] != "3" {
				t.Errorf("a switched-off inbound's site is counted: %v", it.Params)
			}
		case "weak_site":
			if it.Params["sites"] != "home.duckdns.org" {
				t.Errorf("weak: %v", it.Params)
			}
		case "default_site":
			if it.Params["inbounds"] != "vless-xhttp" || it.Params["site"] != "www.microsoft.com" {
				t.Errorf("default: %v", it.Params)
			}
		}
	}
}

// Behind the shared 443 a protocol takes no port of its own to show.
func TestExposureCountsPortsPastTheShared443(t *testing.T) {
	routed := func(name, preset, port string) db.Inbound {
		in := exposureInbound(t, name, preset, port, "news.example.org:443")
		tm, err := proto.Parse(in.Config)
		if err != nil {
			t.Fatal(err)
		}
		tm["mikan"] = map[string]any{"sni443": true}
		in.Config = proto.Marshal(tm)
		return in
	}
	ins := []db.Inbound{routed("a", "vless_reality_xhttp", "9443"), routed("b", "vless_reality_vision", "8443"), routed("c", "vless_reality_grpc", "2053")}
	if got := exposureCodes(exposure{node: db.Node{SniRouter: 1}, inbounds: ins}.items()); !slices.Equal(got, []string{"ok"}) {
		t.Fatalf("all behind the shared 443: %v", got)
	}
	if got := exposureCodes(exposure{inbounds: ins}.items()); !slices.Equal(got, []string{"many_ports"}) {
		t.Fatalf("the shared 443 is off on the node: %v", got)
	}
}

func TestExposureOfACDNOrigin(t *testing.T) {
	cdn := exposureInbound(t, "vless-cdn", presets.PresetCDNXHTTP, "443", "")
	direct := exposureInbound(t, "hysteria2", "hysteria2", "2083", "")
	if got := exposureCodes(exposure{inbounds: []db.Inbound{cdn, direct}}.items()); !slices.Equal(got, []string{"cdn_and_direct"}) {
		t.Fatalf("a CDN's server that clients also reach straight: %v", got)
	}
	if got := exposureCodes(exposure{inbounds: []db.Inbound{cdn}}.items()); !slices.Equal(got, []string{"ok"}) {
		t.Fatalf("a server only a CDN reaches: %v", got)
	}
}

func TestExposureOfARemoteNodesAPI(t *testing.T) {
	if got := exposureCodes(exposure{node: db.Node{Address: "203.0.113.9:40123"}}.items()); !slices.Equal(got, []string{"api_open"}) {
		t.Fatalf("the API port open to anyone: %v", got)
	}
	if got := exposureCodes(exposure{node: db.Node{Address: "203.0.113.9:40123", ApiLockdown: 1}}.items()); !slices.Equal(got, []string{"ok"}) {
		t.Fatalf("the API port for the panel alone: %v", got)
	}
	if got := exposureCodes(exposure{}.items()); !slices.Equal(got, []string{"ok"}) {
		t.Fatalf("the panel's own node has no API port: %v", got)
	}
}

// A name that says what the server is for: the panel's on its own node, a node's own
// anywhere; a remote node does not answer for the panel's name.
func TestExposureOfATellingName(t *testing.T) {
	for _, c := range []struct {
		node  db.Node
		names []string
		want  string
	}{
		{db.Node{}, []string{"vpn.example.com"}, "vpn.example.com"},
		{db.Node{}, []string{"my.example.com", "sub.fastproxy.example"}, "sub.fastproxy.example"},
		{db.Node{Domain: "nl.myvpn.example"}, []string{"my.example.com"}, "nl.myvpn.example"},
		{db.Node{Address: "203.0.113.9:40123", ApiLockdown: 1, Domain: "nl.example.com"}, []string{"vpn.example.com"}, ""},
		{db.Node{}, []string{"my.example.com"}, ""},
	} {
		got := ""
		for _, it := range (exposure{node: c.node, names: c.names}).items() {
			if it.Code == "telling_name" {
				got = it.Params["name"]
			}
		}
		if got != c.want {
			t.Errorf("node %+v, names %v: %q, want %q", c.node.Domain, c.names, got, c.want)
		}
	}
	if w := tellingWord("shop.example.com"); w != "" {
		t.Errorf("an ordinary name: %q", w)
	}
}

// Behind the shared 443 each REALITY protocol has a name of its own, and the panel's own
// names are the server's own certificate: neither is a row of strangers' certificates.
func TestExposureBehindTheSharedPort(t *testing.T) {
	routed := func(name, preset, port, dest string) db.Inbound {
		in := exposureInbound(t, name, preset, port, dest)
		tpl, err := proto.Parse(in.Config)
		if err != nil {
			t.Fatal(err)
		}
		if err := proto.SetSNI443(tpl, true); err != nil {
			t.Fatal(err)
		}
		in.Config = string(tpl.JSON())
		return in
	}
	e := exposure{node: db.Node{SniRouter: 1}, names: []string{"my.example.com"}, inbounds: []db.Inbound{
		routed("vless-xhttp", "vless_reality_xhttp", "20000", "news.example.org:443"),
		routed("vless-vision", "vless_reality_vision", "20001", "shop.example.net:443"),
		routed("vless-grpc", "vless_reality_grpc", "20002", "blog.example.com:443"),
	}}
	if got := exposureCodes(e.items()); !slices.Equal(got, []string{"ok"}) {
		t.Fatalf("three names behind one 443: %v", got)
	}
	// On ports of their own the same three stand out, the panel's own name aside.
	e = exposure{names: []string{"my.example.com"}, inbounds: []db.Inbound{
		exposureInbound(t, "vless-xhttp", "vless_reality_xhttp", "9443", "news.example.org:443"),
		exposureInbound(t, "vless-vision", "vless_reality_vision", "8443", "my.example.com:443"),
	}}
	if got := exposureCodes(e.items()); !slices.Equal(got, []string{"ok"}) {
		t.Fatalf("a stranger's site and the panel's own: %v", got)
	}
}
