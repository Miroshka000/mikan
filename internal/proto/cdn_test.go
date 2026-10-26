package proto

import (
	"slices"
	"testing"
)

// Behind a CDN the node takes the client's address from the CDN's header, from the CDN's
// networks only; Cloudflare's come by name.
func TestCDNGivesTheListenerTheRealAddress(t *testing.T) {
	tpl := mustParse(t, "type: vless\nxhttp-config: {path: /x, mode: packet-up}\nmikan: {tls: node}\n")
	if !CanCDN(tpl) {
		t.Fatal("VLESS TLS XHTTP can go behind a CDN")
	}
	if err := SetCDN(tpl, &CDN{Header: "CF-Connecting-IP", Trusted: []string{"cloudflare", " 203.0.113.0/24 ", ""}}); err != nil {
		t.Fatal(err)
	}
	l, err := Listener(tpl, "cdn", "", "2053", slots, Cert{CertPath: "/c", KeyPath: "/k"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	trusted, _ := l["trusted-proxies"].([]any)
	if l["trusted-proxy-header"] != "CF-Connecting-IP" || !slices.Contains(trusted, any("104.16.0.0/13")) ||
		!slices.Contains(trusted, any("2606:4700::/32")) || trusted[len(trusted)-1] != "203.0.113.0/24" {
		t.Fatalf("listener: %v %v", l["trusted-proxy-header"], trusted)
	}
	if _, ok := l["mikan"]; ok {
		t.Fatal("mikan's section reached the core")
	}

	// A CDN without a header keeps the CDN's addresses: nothing for the core.
	if err := SetCDN(tpl, &CDN{}); err != nil {
		t.Fatal(err)
	}
	l, _ = Listener(tpl, "cdn", "", "2053", slots, Cert{CertPath: "/c", KeyPath: "/k"}, Options{})
	if _, ok := l["trusted-proxy-header"]; ok {
		t.Fatalf("no header, no real address: %v", l)
	}
	if err := SetCDN(tpl, nil); err != nil || CDNOf(tpl) != nil {
		t.Fatalf("out from behind the CDN: %v %v", CDNOf(tpl), err)
	}
	if _, ok := tpl["mikan"].(map[string]any)["cdn"]; ok {
		t.Fatal("the cdn key stays")
	}
}

func TestCDNRefusesWhatACDNCannotCarry(t *testing.T) {
	ok := "type: vless\nws-path: /w\nmikan: {tls: node}\n"
	for src, want := range map[string]string{
		"type: vless\nxhttp-config: {path: /x}\nreality-config: {dest: a.example:443, private-key: x, short-id: [a1], server-names: [a.example]}\n": "cdn_type",
		"type: hysteria2\n": "cdn_type",
		"type: vless\nmikan: {tls: node, flow: xtls-rprx-vision}\n": "cdn_transport",
		"type: trojan\nmikan: {tls: node}\n":                        "cdn_transport",
	} {
		if got := code(SetCDN(mustParse(t, src), &CDN{})); got != want {
			t.Errorf("%q: %q, want %q", src, got, want)
		}
	}
	for c, want := range map[*CDN]string{
		{Header: "CF Connecting"}:                                           "cdn_header",
		{Header: "X-Forwarded-For"}:                                         "cdn_trusted_needed",
		{Header: "X-Real-IP", Trusted: []string{"nope"}}:                    "cdn_trusted",
		{Header: "X-Real-IP", Trusted: []string{"10.0.0.0/33"}}:             "cdn_trusted",
		{Header: "X-Real-IP", Trusted: []string{"private", "198.51.100.7"}}: "",
	} {
		if got := code(SetCDN(mustParse(t, ok), c)); got != want {
			t.Errorf("%+v: %q, want %q", *c, got, want)
		}
	}
	// A template written by hand is checked the same way.
	if got := code(Validate(mustParse(t, "type: trojan\nmikan: {tls: node, cdn: {}}\n"), Options{})); got != "cdn_transport" {
		t.Fatalf("validate: %q", got)
	}
}

// GET uploads are for CDNs that drop POST: packet-up only, the data in the body (the CDN
// must pass a GET's body on), a header or a cookie, and only apps that can send them get
// the inbound.
func TestXHTTPGetUplink(t *testing.T) {
	for x, want := range map[XHTTPTuning]string{
		{UplinkMethod: "GET", Mode: "stream-one"}:                                                        "xhttp_get_mode",
		{UplinkMethod: "GET", Mode: "packet-up"}:                                                         "",
		{UplinkMethod: "GET", Mode: "packet-up", UplinkDataPlacement: "body"}:                            "",
		{UplinkMethod: "GET", Mode: "packet-up", UplinkDataPlacement: "header", UplinkDataKey: "X-Data"}: "",
	} {
		if got := code(x.check()); got != want {
			t.Errorf("%+v: %q, want %q", x, got, want)
		}
	}
	tpl := mustParse(t, "type: vless\nxhttp-config: {path: /x, mode: packet-up, uplink-http-method: GET, uplink-data-placement: header, uplink-data-key: X-Data}\nmikan: {tls: node}\n")
	if err := Validate(tpl, Options{}); err != nil {
		t.Fatal(err)
	}
	if !NeedsOf(tpl).GetUplink {
		t.Fatal("the needs name the GET upload")
	}
}
