package node

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/metacubex/mihomo/common/proxyproto"

	"mikan/internal/nodeapi"
)

// backend is an inbound behind the router: it reads the PROXY header and the first bytes
// after it, and says what it got.
type backendHit struct {
	src   netip.AddrPort
	first byte
}

func backend(t *testing.T) (port string, hits chan backendHit) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	hits = make(chan backendHit, 4)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
				h, err := proxyproto.Read(c)
				if err != nil {
					return
				}
				b := make([]byte, 1)
				if _, err := io.ReadFull(c, b); err != nil {
					return
				}
				hits <- backendHit{src: h.Source, first: b[0]}
			}()
		}
	}()
	_, port, _ = net.SplitHostPort(ln.Addr().String())
	return port, hits
}

// helloTo starts a TLS ClientHello for name on a new connection to addr. The handshake cannot
// finish (the backend speaks no TLS): only the ClientHello counts, so it is started and left.
func helloTo(t *testing.T, addr, name string) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = tls.Client(c, &tls.Config{ServerName: name, InsecureSkipVerify: true}).Handshake() }()
	return c
}

// hitBy waits for the backend to get the ClientHello that c sent: the client address in the
// PROXY header, a TLS record after it. what names the case in a failure.
func hitBy(t *testing.T, hits chan backendHit, c net.Conn, what string) {
	t.Helper()
	select {
	case h := <-hits:
		want, _ := netip.ParseAddrPort(c.LocalAddr().String())
		if h.src != want || h.first != 0x16 {
			t.Fatalf("%s: the backend got src %v first %#x, want %v and a TLS record", what, h.src, h.first, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: nothing reached the backend", what)
	}
}

// The router hands a ClientHello to the inbound its name routes to, with the client's
// address in a PROXY v2 header the core reads, the ClientHello whole after it; another
// name goes to the default inbound; with none, it is closed.
func TestSNIRouter(t *testing.T) {
	aPort, aHits := backend(t)
	bPort, bHits := backend(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := &sniRouter{ln: ln, log: slog.New(slog.DiscardHandler)}
	r.set(&nodeapi.SNIRouter{Routes: []nodeapi.SNIRoute{{Inbound: "a", Names: []string{"A.example.com."}}}, Default: "b"},
		[]nodeapi.Inbound{{Name: "a", Port: aPort}, {Name: "b", Port: bPort}})
	go r.serve(nil)
	defer r.close()
	c := helloTo(t, ln.Addr().String(), "a.example.com")
	hitBy(t, aHits, c, "a routed name")
	c.Close()
	c = helloTo(t, ln.Addr().String(), "unknown.example.org")
	hitBy(t, bHits, c, "the default inbound")
	c.Close()

	r.set(&nodeapi.SNIRouter{Routes: []nodeapi.SNIRoute{{Inbound: "a", Names: []string{"a.example.com"}}}}, []nodeapi.Inbound{{Name: "a", Port: aPort}})
	plain, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	_, _ = plain.Write([]byte("GET / HTTP/1.1\r\n\r\n"))
	_ = plain.SetReadDeadline(time.Now().Add(5 * time.Second))
	if n, err := plain.Read(make([]byte, 1)); err == nil || n != 0 {
		t.Fatalf("a request that is no TLS, with no default: read %d, %v", n, err)
	}
}

func TestProxyV2Parses(t *testing.T) {
	for _, c := range []struct{ src, dst string }{{"203.0.113.7:51234", "198.51.100.1:443"}, {"[2001:db8::7]:51234", "[2001:db8::1]:443"}} {
		src, dst := netip.MustParseAddrPort(c.src), netip.MustParseAddrPort(c.dst)
		pr, pw := net.Pipe()
		go func() { _, _ = pw.Write(proxyV2(src, dst)); pw.Close() }()
		h, err := proxyproto.Read(pr)
		if err != nil || h.Version != 2 || h.Local || h.Source != src || h.Destination != dst {
			t.Fatalf("%s: %+v %v", c.src, h, err)
		}
	}
}

// testCertPEM is a self-signed certificate for name and its key, PEM.
func testCertPEM(t *testing.T, name string) (certPEM, keyPEM string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kder, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}))
}

// The front page behind the router: the node's own name, and a ClientHello with no name,
// get the page over TLS 1.2 or later on the node's certificate, with HSTS; another path is
// a 404. A name routed to an inbound stays the inbound's, any other name goes to the
// default inbound, and with the page off the stub's names go there too.
func TestFrontPageOnTheRouter(t *testing.T) {
	certPEM, keyPEM := testCertPEM(t, "node.example.com")
	aPort, aHits := backend(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	front := newFrontPage()
	defer func() { _ = front.srv.Close(); _ = front.ln.Close() }()
	if front.set("cats", &nodeapi.TLSFiles{CertPEM: certPEM, KeyPEM: keyPEM}); !front.on() {
		t.Fatal("the page does not take the node's certificate")
	}
	r := &sniRouter{ln: ln, log: slog.New(slog.DiscardHandler), front: front}
	r.set(&nodeapi.SNIRouter{Routes: []nodeapi.SNIRoute{{Inbound: "a", Names: []string{"a.example.com"}}}, Default: "a", Stub: []string{"node.example.com", ""}},
		[]nodeapi.Inbound{{Name: "a", Port: aPort}})
	go r.serve(nil)
	defer r.close()

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM([]byte(certPEM))
	// host is the address asked for: a name goes as SNI, an IP address as none at all.
	get := func(host, path string, cfg *tls.Config) (*http.Response, error) {
		t.Helper()
		if cfg == nil {
			cfg = &tls.Config{RootCAs: pool}
		}
		c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: cfg,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "tcp", ln.Addr().String())
			}}}
		return c.Get("https://" + host + path)
	}
	resp, err := get("node.example.com", "/", nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "<html") || resp.Header.Get("Strict-Transport-Security") == "" || resp.TLS.Version < tls.VersionTLS12 {
		t.Fatalf("the page: %d hsts %q tls %#x", resp.StatusCode, resp.Header.Get("Strict-Transport-Security"), resp.TLS.Version)
	}
	if resp, err := get("node.example.com", "/admin", nil); err != nil || resp.StatusCode != http.StatusNotFound {
		t.Fatalf("another path: %v %v", resp, err)
	}
	if resp, err := get("127.0.0.1", "/", &tls.Config{InsecureSkipVerify: true}); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("a ClientHello with no name: %v %v", resp, err)
	}
	if _, err := get("node.example.com", "/", &tls.Config{RootCAs: pool, MaxVersion: tls.VersionTLS11}); err == nil {
		t.Fatal("TLS 1.1 is let in")
	}

	c := helloTo(t, ln.Addr().String(), "a.example.com")
	hitBy(t, aHits, c, "a routed name")
	c.Close()
	c = helloTo(t, ln.Addr().String(), "other.example.org")
	hitBy(t, aHits, c, "any other name")
	c.Close()
	front.set("", nil)
	c = helloTo(t, ln.Addr().String(), "node.example.com")
	hitBy(t, aHits, c, "the page off")
	c.Close()
}
