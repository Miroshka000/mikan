package proto

import (
	"testing"
)

// Behind a TCP proxy (GitHub issue #11) clients get the proxy's address, port and TLS
// name; the node keeps listening where it does.
func TestSetClientEndpoint(t *testing.T) {
	tpl := mustParse(t, "type: trojan\nws-path: /ws\nmikan:\n  tls: node\n  client:\n    fingerprint: ios\n")
	if err := SetClientEndpoint(tpl, "vpn.example.com", 443, "trojan.example.com", ""); err != nil {
		t.Fatal(err)
	}
	got := mustParse(t, Marshal(tpl))
	if c := got.Ext().Client; c.Server != "vpn.example.com" || c.Port != 443 || c.SNI != "trojan.example.com" || c.Fingerprint != "ios" {
		t.Fatalf("set: %+v\n%s", c, Marshal(tpl))
	}
	if err := Validate(got, Options{}); err != nil {
		t.Fatal(err)
	}

	// Clearing all three keeps the other overrides.
	if err := SetClientEndpoint(tpl, "", 0, "", ""); err != nil {
		t.Fatal(err)
	}
	if c := tpl.Ext().Client; c != (ClientOverrides{Fingerprint: "ios"}) {
		t.Fatalf("clear: %+v", c)
	}
	if err := SetFingerprint(tpl, ""); err != nil {
		t.Fatal(err)
	}
	if e, ok := tpl[extKey].(map[string]any); !ok || e["client"] != nil || e["tls"] != "node" {
		t.Fatalf("empty client section left: %v", tpl[extKey])
	}

	for _, c := range []struct {
		server, sni string
		port        int
		code        string
	}{
		{"vpn example.com", "", 0, "config_client_server"},
		{"localhost", "", 0, "config_client_server"},
		{"a,DIRECT", "", 0, "config_client_server"},
		{"", "", 65536, "config_client_port"},
		{"", "", -1, "config_client_port"},
		{"", "203.0.113.9", 0, "config_client_sni"}, // SNI is a name, never an address
		{"", "bad_name.example.com", 0, "config_client_sni"},
	} {
		if got := code(SetClientEndpoint(tpl, c.server, c.port, c.sni, "")); got != c.code {
			t.Errorf("%+v: %q, want %q", c, got, c.code)
		}
	}
	// An IPv6 proxy address is one too.
	if err := SetClientEndpoint(tpl, "2001:db8::1", 443, "", ""); err != nil || tpl.Ext().Client.Server != "2001:db8::1" {
		t.Fatalf("ipv6: %v %+v", err, tpl.Ext().Client)
	}
}

// A REALITY client sends the target's name and the node checks it, so the SNI there is
// not the client side's to change; QUIC protocols on the node certificate take one.
func TestClientSNIByProtocol(t *testing.T) {
	priv, _ := realityKey(t)
	reality := mustParse(t, "type: vless\nreality-config:\n  dest: www.microsoft.com:443\n  private-key: "+priv+"\n  short-id: [a1b2]\n  server-names: [www.microsoft.com]\n")
	if ClientSNI(reality) || code(SetClientEndpoint(reality, "vpn.example.com", 443, "other.example.com", "")) != "config_client_sni" {
		t.Fatal("REALITY must refuse an SNI of the client side's own")
	}
	if err := SetClientEndpoint(reality, "vpn.example.com", 443, "", ""); err != nil {
		t.Fatalf("REALITY still takes the address and port: %v", err)
	}
	for src, want := range map[string]bool{"type: hysteria2\n": true, "type: anytls\n": true, "type: mieru\ntransport: TCP\n": false} {
		if got := ClientSNI(mustParse(t, src)); got != want {
			t.Errorf("%q: %v, want %v", src, got, want)
		}
	}
}

// Only VLESS and Trojan over WebSocket or XHTTP send an HTTP Host of the client side's own.
func TestClientHostByProtocol(t *testing.T) {
	for src, want := range map[string]bool{
		"type: vless\nws-path: /w\nmikan: {tls: node}\n":                               true,
		"type: vless\nxhttp-config: {path: /x, mode: packet-up}\nmikan: {tls: node}\n": true,
		"type: trojan\nmikan: {tls: node}\n":                                           false,
		"type: hysteria2\n":                                                            false,
	} {
		if got := ClientHost(mustParse(t, src)); got != want {
			t.Errorf("%q: %v, want %v", src, got, want)
		}
	}
	if code(SetClientEndpoint(mustParse(t, "type: hysteria2\n"), "", 0, "", "h.example.com")) != "config_client_host" {
		t.Fatal("Hysteria2 sends no HTTP Host")
	}
}
