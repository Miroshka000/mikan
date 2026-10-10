package nodetls

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func handshake(t *testing.T, server, client *tls.Config) error {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", server)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		if err := c.(*tls.Conn).Handshake(); err == nil {
			_, _ = c.Write([]byte("ok"))
		}
	}()
	c, err := tls.DialWithDialer(&net.Dialer{Timeout: 3 * time.Second}, "tcp", ln.Addr().String(), client)
	if err != nil {
		return err
	}
	defer c.Close()
	// TLS 1.3 reports a rejected client certificate on the first read.
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 2)
	_, err = io.ReadFull(c, buf)
	return err
}

func TestPinnedHandshake(t *testing.T) {
	now := time.Now()
	panel, err := Generate("mikan-panel", x509.ExtKeyUsageClientAuth, now)
	if err != nil {
		t.Fatal(err)
	}
	node, err := Generate("node-2.mikan", x509.ExtKeyUsageServerAuth, now)
	if err != nil {
		t.Fatal(err)
	}
	panelPin, _ := Fingerprint(panel.CertPEM)
	nodePin, _ := Fingerprint(node.CertPEM)

	raw, err := Key{Port: 40123, PanelPin: panelPin, CertPEM: node.CertPEM, KeyPEM: node.KeyPEM}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(raw, " '\"$`\n") {
		t.Fatalf("the key must paste into a shell as one word: %q", raw)
	}
	key, err := DecodeKey(raw)
	if err != nil || key.Port != 40123 {
		t.Fatalf("decode: %+v %v", key, err)
	}
	server, err := key.ServerConfig()
	if err != nil {
		t.Fatal(err)
	}
	client, err := ClientConfig(panel, nodePin)
	if err != nil {
		t.Fatal(err)
	}
	if err := handshake(t, server, client); err != nil {
		t.Fatalf("panel must reach its node: %v", err)
	}

	// Another panel (or a leaked node key used as a client) is refused by the node.
	stranger, _ := Generate("mikan-panel", x509.ExtKeyUsageClientAuth, now)
	other, _ := ClientConfig(stranger, nodePin)
	if handshake(t, server, other) == nil {
		t.Fatal("a foreign client certificate must be refused")
	}
	asClient, _ := ClientConfig(node, nodePin)
	if handshake(t, server, asClient) == nil {
		t.Fatal("the node's own certificate must not work as the panel's")
	}
	// The panel refuses a node whose certificate is not the pinned one (a re-issued key).
	fresh, _ := Generate("node-2.mikan", x509.ExtKeyUsageServerAuth, now)
	impostor, _ := Key{Port: 40123, PanelPin: panelPin, CertPEM: fresh.CertPEM, KeyPEM: fresh.KeyPEM}.ServerConfig()
	if handshake(t, impostor, client) == nil {
		t.Fatal("a node certificate that is not pinned must be refused")
	}
}

func TestDecodeKeyRejectsGarbage(t *testing.T) {
	for _, s := range []string{"", "mikan1.", "mikan1.!!!", "abc", "mikan1.eyJwb3J0IjowfQ"} {
		if _, err := DecodeKey(s); err == nil {
			t.Errorf("%q must be refused", s)
		}
	}
}

// A key carries the panel's address for the node's hello; keys of older panels have none
// and still work, and an address that is not a panel's is dropped, not fatal.
func TestKeyPanelURL(t *testing.T) {
	now := time.Now()
	panel, _ := Generate("mikan-panel", x509.ExtKeyUsageClientAuth, now)
	node, _ := Generate("node-2.mikan", x509.ExtKeyUsageServerAuth, now)
	panelPin, _ := Fingerprint(panel.CertPEM)
	for url, want := range map[string]string{
		"https://panel.example.com:31000": "https://panel.example.com:31000",
		"https://203.0.113.5:31000/":      "https://203.0.113.5:31000/",
		"":                                "",
		"http://panel.example.com":        "",
		"https://u:p@panel.example.com":   "",
		"https://panel.example.com/admin": "",
		"https://panel.example.com/?a=b":  "",
		"javascript:alert(1)":             "",
	} {
		raw, err := Key{Port: 40123, PanelPin: panelPin, CertPEM: node.CertPEM, KeyPEM: node.KeyPEM, PanelURL: url}.Encode()
		if err != nil {
			t.Fatal(err)
		}
		k, err := DecodeKey(raw)
		if err != nil || k.PanelURL != want {
			t.Errorf("%q: %q, %v", url, k.PanelURL, err)
		}
	}
	// The node's own address rides along, for the installer's advice on port 80.
	raw, _ := Key{Port: 40123, PanelPin: panelPin, CertPEM: node.CertPEM, KeyPEM: node.KeyPEM, Host: "se.example.com"}.Encode()
	if k, err := DecodeKey(raw); err != nil || k.Host != "se.example.com" {
		t.Fatalf("host: %q %v", k.Host, err)
	}
}

func TestSignVerify(t *testing.T) {
	node, _ := Generate("node-2.mikan", x509.ExtKeyUsageServerAuth, time.Now())
	other, _ := Generate("node-3.mikan", x509.ExtKeyUsageServerAuth, time.Now())
	sig, err := Sign(node.KeyPEM, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	pin, err := Verify(node.CertPEM, []byte("hello"), sig)
	want, _ := Fingerprint(node.CertPEM)
	if err != nil || pin != want {
		t.Fatalf("verify: %q %v", pin, err)
	}
	if _, err := Verify(node.CertPEM, []byte("hellO"), sig); err == nil {
		t.Fatal("another message passed")
	}
	if _, err := Verify(other.CertPEM, []byte("hello"), sig); err == nil {
		t.Fatal("another certificate passed")
	}
	if !SamePin(want, want) || SamePin(want, want[:10]) {
		t.Fatal("SamePin")
	}
}
