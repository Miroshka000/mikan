package node

import (
	"testing"

	"github.com/metacubex/mihomo/listener"

	"mikan/internal/nodeapi"
	"mikan/internal/proto"
)

// VLESS on the node certificate, over XHTTP, WebSocket and with Vision, becomes a listener mihomo
// itself accepts; without a certificate the node refuses it instead of serving plain VLESS.
func TestVLESSTLSListener(t *testing.T) {
	slots := []nodeapi.Slot{{Name: "s1", UUID: "00000000-0000-4000-8000-000000000001", Secret: "x"}}
	cert := proto.Cert{CertPath: "/tmp/c.pem", KeyPath: "/tmp/k.pem"}
	for name, config := range map[string]string{
		"xhttp":  `{"type":"vless","xhttp-config":{"path":"/p","mode":"stream-one"},"mikan":{"tls":"node"}}`,
		"vision": `{"type":"vless","mikan":{"flow":"xtls-rprx-vision","tls":"node"}}`,
		"ws":     `{"type":"vless","ws-path":"/w","mikan":{"tls":"node"}}`,
	} {
		in := nodeapi.Inbound{Name: "vless-tls-" + name, Port: "2443", Config: []byte(config)}
		l, err := listenerFor(in, slots, cert, proto.Options{})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if l["certificate"] != cert.CertPath || l["private-key"] != cert.KeyPath {
			t.Fatalf("%s: %v", name, l)
		}
		if _, err := listener.ParseListener(l); err != nil {
			t.Fatalf("%s: mihomo refuses it: %v", name, err)
		}
		if _, err := listenerFor(in, slots, proto.Cert{}, proto.Options{}); err == nil {
			t.Fatalf("%s: a listener without a certificate", name)
		}
	}
}
