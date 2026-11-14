package node

import (
	"fmt"
	"testing"

	"github.com/metacubex/mihomo/listener"

	"mikan/internal/nodeapi"
	"mikan/internal/proto"
)

// A Gecko template becomes a Hysteria2 listener mihomo itself accepts, sizes included.
func TestGeckoListener(t *testing.T) {
	in := nodeapi.Inbound{Name: "hysteria2-gecko", Port: "2443",
		Config: []byte(`{"type":"hysteria2","alpn":["h3"],"obfs":"gecko","obfs-password":"pw","obfs-min-packet-size":600,"obfs-max-packet-size":1100}`)}
	l, err := listenerFor(in, []nodeapi.Slot{{Name: "s1", UUID: "00000000-0000-4000-8000-000000000001", Secret: "x"}},
		proto.Cert{CertPath: "/tmp/c.pem", KeyPath: "/tmp/k.pem"}, proto.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if l["obfs"] != "gecko" || fmt.Sprint(l["obfs-min-packet-size"]) != "600" || fmt.Sprint(l["obfs-max-packet-size"]) != "1100" {
		t.Fatalf("listener: %v", l)
	}
	if _, err := listener.ParseListener(l); err != nil {
		t.Fatalf("mihomo refuses it: %v", err)
	}
}

// The windows the node gives a Hysteria2 listener are ones the core itself accepts.
func TestHysteria2WindowsListener(t *testing.T) {
	in := nodeapi.Inbound{Name: "hysteria2", Port: "443", Config: []byte(`{"type":"hysteria2","alpn":["h3"],"obfs":"salamander","obfs-password":"pw"}`)}
	l, err := listenerFor(in, []nodeapi.Slot{{Name: "s1", UUID: "00000000-0000-4000-8000-000000000001", Secret: "x"}},
		proto.Cert{CertPath: "/tmp/c.pem", KeyPath: "/tmp/k.pem"}, proto.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if l["initial-stream-receive-window"] == nil || l["initial-connection-receive-window"] == nil {
		t.Fatalf("listener: %v", l)
	}
	if _, err := listener.ParseListener(l); err != nil {
		t.Fatalf("the core refuses it: %v", err)
	}
}
