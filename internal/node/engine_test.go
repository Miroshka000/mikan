package node

import (
	"encoding/json"
	"net"
	"runtime/debug"
	"testing"

	"mikan/internal/nodeapi"
)

func TestConnLinesStayOutOfLogs(t *testing.T) {
	for _, msg := range []string{
		"[TCP] dial DIRECT (match Match/) 203.0.113.9:57314 --> [2001:db8::a]:443 error: connect: network is unreachable",
		"[UDP] dial DIRECT (match Match/) 203.0.113.9:4000 --> example.com:443 error: dns resolve failed",
		// The core's egress guard names the user and where they went.
		"[TCP] dial DIRECT (match Match/) 203.0.113.9:57314 --> 198.51.100.7:5432 error: egress guard blocked 198.51.100.7: address of this host, not reachable from inbound vless (user slot-1)",
		"[UDP] egress guard blocked 198.51.100.7: address of this host, not reachable from inbound hy2 (user slot-1), dropping the datagrams of this session to blocked addresses",
	} {
		if !connLine(msg) {
			t.Errorf("per-connection line must be dropped: %q", msg)
		}
	}
	for _, msg := range []string{
		"Listener vless-vision listen err: listen tcp :443: bind: address already in use",
		"mikan-sync 00ff",
	} {
		if connLine(msg) {
			t.Errorf("line must be kept: %q", msg)
		}
	}
}

// A port another program holds reaches the panel as a code, whatever the OS calls it: the
// error here is this OS's own, from a second listen on a taken port, over TCP and UDP.
func TestBusyPortIsReportedByCode(t *testing.T) {
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	_, errTCP := net.Listen("tcp", tcp.Addr().String())
	_, errUDP := net.ListenPacket("udp", udp.LocalAddr().String())
	for _, e := range []error{errTCP, errUDP} {
		if e == nil {
			t.Fatal("a second listen on a taken port succeeded")
		}
		ls := listenFailed("vless-xhttp", e.Error())
		if ls.OK || ls.Code != nodeapi.ListenerAddrInUse || !ls.Busy() || ls.Error != e.Error() {
			t.Fatalf("status of %q: %+v", e, ls)
		}
	}
	for _, msg := range []string{"listen tcp: address 99999: invalid port", "listen tcp :443: bind: permission denied", "tls: no certificate"} {
		if ls := listenFailed("tuic", msg); ls.Code != "" || ls.Busy() {
			t.Errorf("%q is not a busy port: %+v", msg, ls)
		}
	}
	// The code is new: a status without one keeps the JSON older panels read.
	raw, _ := json.Marshal(nodeapi.ListenerStatus{Name: "tuic", OK: true})
	if string(raw) != `{"name":"tuic","ok":true}` {
		t.Fatalf("JSON: %s", raw)
	}
}

func TestCoreName(t *testing.T) {
	for _, c := range []struct {
		m    debug.Module
		want string
	}{
		{debug.Module{Version: "v1.19.32"}, "mihomo v1.19.32"},
		{debug.Module{Version: "v1.19.32", Replace: &debug.Module{Path: "github.com/getmikan/Citrus", Version: "v1.19.33-0.20261018104129-2cae038ea2a8"}}, "citrus 2cae038"},
		{debug.Module{Version: "v1.19.32", Replace: &debug.Module{Path: "github.com/getmikan/Citrus", Version: "v1.0.0"}}, "citrus v1.0.0"},
		{debug.Module{Version: "v1.19.32", Replace: &debug.Module{Path: "github.com/getmikan/Citrus", Version: "v1.0.0-beta.1"}}, "citrus v1.0.0-beta.1"},
	} {
		if got := coreName(&c.m); got != c.want {
			t.Errorf("coreName(%+v) = %q, want %q", c.m.Replace, got, c.want)
		}
	}
}
