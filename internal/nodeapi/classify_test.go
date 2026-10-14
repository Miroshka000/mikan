package nodeapi_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"syscall"
	"testing"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/nodetls"
)

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

// The classification of errors that are hard to make for real on a test machine: a firewall
// that drops packets, a network with no route, a name that does not resolve.
func TestClassifyErrors(t *testing.T) {
	op := func(err error) error {
		return fmt.Errorf("%w: %w", nodeapi.ErrUnavailable, &net.OpError{Op: "dial", Net: "tcp", Err: err})
	}
	cases := []struct {
		err  error
		want string
	}{
		{nil, ""},
		{op(timeoutErr{}), nodeapi.LinkTimeout},
		{op(os.ErrDeadlineExceeded), nodeapi.LinkTimeout},
		{fmt.Errorf("get: %w", context.DeadlineExceeded), nodeapi.LinkTimeout},
		{op(&os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}), nodeapi.LinkRefused},
		{op(&os.SyscallError{Syscall: "connect", Err: syscall.EHOSTUNREACH}), nodeapi.LinkUnreachable},
		{op(&os.SyscallError{Syscall: "connect", Err: syscall.ENETUNREACH}), nodeapi.LinkUnreachable},
		{op(&net.DNSError{Err: "no such host", Name: "node.example", IsNotFound: true}), nodeapi.LinkDNS},
		{op(&net.DNSError{Err: "timeout", Name: "node.example", IsTimeout: true}), nodeapi.LinkTimeout},
		{&nodeapi.StatusError{Method: "GET", Path: "/v1/health", Status: 502}, nodeapi.LinkHTTPStatus},
		{errors.New("net/http: TLS handshake timeout"), nodeapi.LinkTLS},
		// Words only: an error that lost its type on the way.
		{errors.New("node unavailable: dial tcp 203.0.113.1:1: connect: connection refused"), nodeapi.LinkRefused},
		{errors.New("node unavailable: dial tcp 203.0.113.1:1: connect: no route to host"), nodeapi.LinkUnreachable},
		{errors.New("something new"), nodeapi.LinkUnknown},
	}
	for _, c := range cases {
		if got := nodeapi.Classify(c.err); got != c.want {
			t.Errorf("%v: %q, want %q", c.err, got, c.want)
		}
	}
}

// What the panel's client really gets from a closed port, a stranger on the port, a node
// with another key, and a node that refuses the panel.
func TestClassifyRealConnections(t *testing.T) {
	now := time.Now()
	panel, err := nodetls.Generate("mikan-panel", x509.ExtKeyUsageClientAuth, now)
	if err != nil {
		t.Fatal(err)
	}
	node, err := nodetls.Generate("node-1.mikan", x509.ExtKeyUsageServerAuth, now)
	if err != nil {
		t.Fatal(err)
	}
	other, err := nodetls.Generate("node-2.mikan", x509.ExtKeyUsageServerAuth, now)
	if err != nil {
		t.Fatal(err)
	}
	pin := func(p nodetls.Pair) string {
		f, err := nodetls.Fingerprint(p.CertPEM)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	serve := func(key nodetls.Key, h http.Handler) string {
		cfg, err := key.ServerConfig()
		if err != nil {
			t.Fatal(err)
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		srv := &http.Server{Handler: h, ErrorLog: nil}
		go func() { _ = srv.Serve(tls.NewListener(ln, cfg)) }()
		t.Cleanup(func() { _ = srv.Close() })
		return ln.Addr().String()
	}
	health := func(addr, nodePin string) error {
		cfg, err := nodetls.ClientConfig(panel, nodePin)
		if err != nil {
			t.Fatal(err)
		}
		_, err = nodeapi.NewTLSClient(addr, cfg).Health(context.Background())
		return err
	}
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"version":"1"}`)) })
	broken := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })

	good := serve(nodetls.Key{PanelPin: pin(panel), CertPEM: node.CertPEM, KeyPEM: node.KeyPEM}, ok)
	if err := health(good, pin(node)); err != nil {
		t.Fatalf("the node itself: %v", err)
	}
	if got := nodeapi.Classify(health(good, pin(other))); got != nodeapi.LinkPinMismatch {
		t.Errorf("another node's certificate: %q", got)
	}
	// The node holds a key of another panel: it refuses this panel's certificate.
	strange := serve(nodetls.Key{PanelPin: pin(other), CertPEM: node.CertPEM, KeyPEM: node.KeyPEM}, ok)
	if got := nodeapi.Classify(health(strange, pin(node))); got != nodeapi.LinkPinMismatch {
		t.Errorf("a node of another panel: %q", got)
	}
	failing := serve(nodetls.Key{PanelPin: pin(panel), CertPEM: node.CertPEM, KeyPEM: node.KeyPEM}, broken)
	if got := nodeapi.Classify(health(failing, pin(node))); got != nodeapi.LinkHTTPStatus {
		t.Errorf("an error status: %q", got)
	}
	web := httptest.NewServer(ok)
	defer web.Close()
	if got := nodeapi.Classify(health(web.Listener.Addr().String(), pin(node))); got != nodeapi.LinkTLS {
		t.Errorf("plain HTTP on the port: %q", got)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := ln.Addr().String()
	ln.Close()
	if got := nodeapi.Classify(health(closed, pin(node))); got != nodeapi.LinkRefused {
		t.Errorf("a closed port: %q", got)
	}
}

func TestLinkParams(t *testing.T) {
	p := nodeapi.LinkParams(nodeapi.LinkHTTPStatus, "203.0.113.5:31234", &nodeapi.StatusError{Status: 503})
	if p["host"] != "203.0.113.5" || p["port"] != "31234" || p["status"] != "503" {
		t.Fatalf("params: %v", p)
	}
}
