package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"testing"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/store/db"
)

func checkInbound(name, port, listen, cfg string, on bool) db.Inbound {
	in := db.Inbound{Name: name, Port: port, Listen: listen, Config: cfg}
	if on {
		in.Enabled = 1
	}
	return in
}

// The ports the node check dials: every enabled TCP protocol at its first port, one on an
// address of its own with that address (a proxy on the server stands in front of it), the
// ones behind the shared 443 as the node's port 443 once, and the relay; no UDP, nothing off.
func TestNodeTCPPorts(t *testing.T) {
	ins := []db.Inbound{
		checkInbound("open", "8443", "", `{"type":"vless","reality-config":{"server-names":["a.example"]}}`, true),
		checkInbound("nginx", "444", "127.0.0.1", `{"type":"vless","xhttp-config":{"path":"/x"}}`, true),
		checkInbound("reality", "9443", "", `{"type":"vless","reality-config":{"server-names":["b.example"]},"mikan":{"sni443":true}}`, true),
		checkInbound("tls", "9444", "", `{"type":"trojan","mikan":{"tls":"node","sni443":true}}`, true),
		checkInbound("hy2", "20000-20100", "", `{"type":"hysteria2"}`, true),
		checkInbound("off", "7443", "", `{"type":"vless"}`, false),
	}
	got := nodeTCPPorts(db.Node{SniRouter: 1}, ins, "4500")
	want := []tcpPort{{"open", 8443, ""}, {"nginx", 444, "127.0.0.1"}, {"reality, tls", 443, ""}, {"relay", 4500, ""}}
	if !slices.Equal(got, want) {
		t.Fatalf("with the router:\n got %+v\nwant %+v", got, want)
	}
	// Without the router the marked ones listen on their own ports.
	got = nodeTCPPorts(db.Node{}, ins, "")
	want = []tcpPort{{"open", 8443, ""}, {"nginx", 444, "127.0.0.1"}, {"reality", 9443, ""}, {"tls", 9444, ""}}
	if !slices.Equal(got, want) {
		t.Fatalf("without the router:\n got %+v\nwant %+v", got, want)
	}
	// A protocol of its own on 443 is that port already: the router's 443 is not dialed twice.
	ins = append(ins, checkInbound("own443", "443", "", `{"type":"vless","reality-config":{"server-names":["c.example"]}}`, true))
	if got := nodeTCPPorts(db.Node{SniRouter: 1}, ins, ""); slices.ContainsFunc(got, func(p tcpPort) bool { return p.name == "reality, tls" }) {
		t.Fatalf("443 twice: %+v", got)
	}
}

// A port on an address of its own that is not public is not dialed from outside, whatever
// answers there: a proxy on the node's server (nginx, HAProxy) leads to it.
func TestDialPorts(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	items := dialPorts(context.Background(), "127.0.0.1", []tcpPort{{"open", port, ""}, {"nginx", port, "127.0.0.1"}, {"lan", port, "10.0.0.5"}})
	if len(items) != 3 || items[0].Status != nodeapi.CheckOK || items[0].Params["port"] != strconv.Itoa(port) {
		t.Fatalf("the open port: %+v", items)
	}
	for i, listen := range []string{"127.0.0.1", "10.0.0.5"} {
		it := items[i+1]
		if it.Status != nodeapi.CheckSkip || it.Code != "behind_proxy" || it.Params["listen"] != listen || it.Fix != "" {
			t.Fatalf("%s: %+v", listen, it)
		}
	}
}

// From Russia the panel asks for the API port and the ports strangers reach: not the relay,
// not one behind a proxy of the server's own, each port once, up to the probe's limit.
func TestRussiaPorts(t *testing.T) {
	ports := []tcpPort{{"nginx", 444, "127.0.0.1"}, {"open", 8443, ""}, {"relay", 4500, ""}, {"twin", 8443, ""}, {"reality, tls", 443, ""}, {"more", 9000, ""}}
	got := russiaPorts(26821, ports)
	want := []RussiaPort{{Port: 26821, Name: "api"}, {Port: 8443, Name: "open"}, {Port: 443, Name: "reality, tls"}}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	// The panel's own node has no API port to ask for.
	if got := russiaPorts(0, ports); len(got) != 3 || got[0].Name != "open" {
		t.Fatalf("own node: %+v", got)
	}
}

// The relay's open connections are told by source node, smallest id first; a name that is
// no relay user's (or a source with none open) is left out.
func TestCascadeConns(t *testing.T) {
	got := cascadeConns(map[string]int{"relay-7": 2, "relay-3": 5, "s1": 9, "relay-9": 0})
	want := []CascadeConns{{NodeID: 3, Conns: 5}, {NodeID: 7, Conns: 2}}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if got := cascadeConns(nil); got == nil || len(got) != 0 {
		t.Fatalf("none: %#v", got)
	}
}

// A probe the entry node did not answer says why: the exit is not in its applied config yet,
// the node predates /v1/probe, or the call itself failed. Only the old node is told to update.
func TestProbeErrItem(t *testing.T) {
	params := map[string]string{"exit": "se", "exit_id": "2"}
	for name, tc := range map[string]struct {
		err               error
		status, code, fix string
		detail            bool
	}{
		"not applied":  {&nodeapi.Error{Code: "no_such_outbound", Message: "not an outbound of this node"}, nodeapi.CheckWarn, "not_applied", "", false},
		"old node 404": {&nodeapi.StatusError{Method: "GET", Path: "/v1/probe", Status: 404}, nodeapi.CheckSkip, "old_node", "update_node", false},
		"old node 405": {&nodeapi.StatusError{Method: "GET", Path: "/v1/probe", Status: 405}, nodeapi.CheckSkip, "old_node", "update_node", false},
		"node error":   {&nodeapi.StatusError{Method: "GET", Path: "/v1/probe", Status: 502}, nodeapi.CheckSkip, "probe_failed", "", true},
		"other code":   {&nodeapi.Error{Code: "not_ready", Message: "starting"}, nodeapi.CheckSkip, "probe_failed", "", true},
		"no link":      {fmt.Errorf("%w: %w", nodeapi.ErrUnavailable, errors.New("dial tcp: i/o timeout")), nodeapi.CheckSkip, "probe_failed", "", true},
	} {
		t.Run(name, func(t *testing.T) {
			it := probeErrItem(params, tc.err)
			if it.ID != "cascade" || it.Status != tc.status || it.Code != tc.code || it.Fix != tc.fix || (it.Detail != "") != tc.detail || it.Params["exit"] != "se" {
				t.Fatalf("%+v", it)
			}
			if tc.detail && it.Detail != tc.err.Error() {
				t.Fatalf("detail %q, want the error's words", it.Detail)
			}
		})
	}
}
