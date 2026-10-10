package certcheck

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"mikan/internal/acmechallenge"
	"mikan/internal/panel/acme"
	"mikan/internal/panel/dnscheck"
)

type pki struct {
	root, inter       *x509.Certificate
	rootKey, interKey *ecdsa.PrivateKey
	pool              *x509.CertPool
}

func newPKI(t *testing.T) *pki {
	t.Helper()
	p := &pki{}
	p.rootKey, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	p.interKey, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ca := func(name string, key *ecdsa.PrivateKey, parent *x509.Certificate, parentKey *ecdsa.PrivateKey) *x509.Certificate {
		tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name, Organization: []string{"Test CA"}},
			NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(365 * 24 * time.Hour), IsCA: true, BasicConstraintsValid: true,
			KeyUsage: x509.KeyUsageCertSign}
		if parent == nil {
			parent, parentKey = tmpl, key
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, parentKey)
		if err != nil {
			t.Fatal(err)
		}
		c, _ := x509.ParseCertificate(der)
		return c
	}
	p.root = ca("Test Root", p.rootKey, nil, nil)
	p.inter = ca("Test R1", p.interKey, p.root, p.rootKey)
	p.pool = x509.NewCertPool()
	p.pool.AddCert(p.root)
	return p
}

// leaf issues a certificate for name; self: signed by itself.
func (p *pki) leaf(t *testing.T, name string, until time.Time, self bool) tls.Certificate {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: until, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	if ip := net.ParseIP(name); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{name}
	}
	parent, parentKey := p.inter, p.interKey
	if self {
		parent, parentKey = tmpl, k
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &k.PublicKey, parentKey)
	if err != nil {
		t.Fatal(err)
	}
	c := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: k}
	if !self {
		c.Certificate = append(c.Certificate, p.inter.Raw)
	}
	c.Leaf, _ = x509.ParseCertificate(der)
	return c
}

func serve(t *testing.T, c tls.Certificate) int {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.NotFoundHandler())
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{c}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().(*net.TCPAddr).Port
}

type fakeDNS map[string][]netip.Addr

func (f fakeDNS) Lookup(_ context.Context, name string) ([]netip.Addr, error) {
	if a, ok := f[name]; ok {
		return a, nil
	}
	return nil, dnscheck.ErrNotFound
}

// checker dials 127.0.0.1 whatever the host, as if the world reached this server.
func checker(p *pki, dns fakeDNS) *Checker {
	return &Checker{DNS: dns, Roots: p.pool, Now: time.Now,
		Dial: func(ctx context.Context, addr, sni string) ([]*x509.Certificate, error) {
			_, port, _ := net.SplitHostPort(addr)
			return dialChain(ctx, net.JoinHostPort("127.0.0.1", port), sni)
		},
		Fetch: func(context.Context, string) (int, string, string, error) { return 0, "", "", errors.New("unused") },
	}
}

func find(t *testing.T, r CertReport, id string) CertCheck {
	t.Helper()
	for _, c := range r.Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no %s check in %+v", id, r.Checks)
	return CertCheck{}
}

// What the panel's port presents, as a client sees it.
func TestServed(t *testing.T) {
	p := newPKI(t)
	good := p.leaf(t, "vpn.example.com", time.Now().Add(60*24*time.Hour), false)
	soon := p.leaf(t, "vpn.example.com", time.Now().Add(2*24*time.Hour), false)
	self := p.leaf(t, "vpn.example.com", time.Now().Add(60*24*time.Hour), true)
	other := p.leaf(t, "other.example.com", time.Now().Add(60*24*time.Hour), false)
	bare := p.leaf(t, "vpn.example.com", time.Now().Add(60*24*time.Hour), false)
	bare.Certificate = bare.Certificate[:1] // no intermediate
	for _, c := range []struct {
		name     string
		cert     tls.Certificate
		expected *x509.Certificate
		status   string
		code     string
	}{
		{"trusted", good, good.Leaf, OK, "served_ok"},
		{"expiring", soon, soon.Leaf, Warn, "served_expiring"},
		{"self-signed", self, self.Leaf, Warn, "served_self_signed"},
		{"another name", other, other.Leaf, Fail, "served_wrong_host"},
		{"no intermediate", bare, bare.Leaf, Fail, "served_chain_incomplete"},
		{"someone else's (nginx with certbot)", good, other.Leaf, Fail, "served_foreign"},
	} {
		port := serve(t, c.cert)
		r := checker(p, fakeDNS{}).Run(context.Background(), Input{Domain: "vpn.example.com", Host: "vpn.example.com", Ports: []int{port}, Expected: c.expected})
		got := find(t, r, "served")
		if got.Status != c.status || got.Code != c.code {
			t.Errorf("%s: %s %s (%v)", c.name, got.Status, got.Code, got.Params)
		}
	}
	// Nothing listens.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	r := checker(p, fakeDNS{}).Run(context.Background(), Input{Domain: "vpn.example.com", Host: "vpn.example.com", Ports: []int{port}})
	if got := find(t, r, "served"); got.Status != Fail || got.Code != "served_unreachable" || got.Detail == "" {
		t.Errorf("unreachable: %+v", got)
	}
}

func TestDNS(t *testing.T) {
	own := []netip.Addr{netip.MustParseAddr("198.51.100.7")}
	p := newPKI(t)
	for _, c := range []struct {
		dns  fakeDNS
		code string
	}{
		{fakeDNS{"vpn.example.com": {netip.MustParseAddr("198.51.100.7")}}, "dns_ok"},
		{fakeDNS{"vpn.example.com": {netip.MustParseAddr("104.21.32.1")}}, "dns_cloudflare"},
		{fakeDNS{"vpn.example.com": {netip.MustParseAddr("198.51.100.7"), netip.MustParseAddr("203.0.113.9")}}, "dns_elsewhere"},
		{fakeDNS{}, "dns_none"},
	} {
		r := checker(p, c.dns).Run(context.Background(), Input{Domain: "vpn.example.com", Host: "vpn.example.com", Own: own})
		if got := find(t, r, "dns"); got.Code != c.code {
			t.Errorf("%v: %s", c.dns, got.Code)
		}
	}
	r := checker(p, fakeDNS{}).Run(context.Background(), Input{Host: "198.51.100.7", Own: own})
	if got := find(t, r, "dns"); got.Code != "dns_ip_only" || got.Status != Info {
		t.Errorf("IP: %+v", got)
	}
}

// Port 80: the check's own token is fetched through the public address; what comes back
// tells a firewall from another web server.
func TestPort80(t *testing.T) {
	p := newPKI(t)
	ch := acmechallenge.New("127.0.0.1:0")
	t.Cleanup(ch.Close)
	in := Input{Host: "198.51.100.7", Challenge: ch, Status: acme.Status{Kind: "acme"}}

	c := checker(p, fakeDNS{})
	c.Fetch = func(ctx context.Context, url string) (int, string, string, error) {
		path := url[strings.Index(url, acmechallenge.Prefix):]
		resp, err := http.Get("http://" + ch.Bound() + path)
		if err != nil {
			return 0, "", "", err
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body), "", nil
	}
	if got := find(t, c.Run(context.Background(), in), "port80"); got.Code != "port80_ok" {
		t.Fatalf("reachable: %+v", got)
	}
	if ch.Listening() {
		t.Fatal("the check left port 80 held")
	}

	c.Fetch = func(context.Context, string) (int, string, string, error) {
		return 0, "", "", errors.New("i/o timeout")
	}
	got := find(t, c.Run(context.Background(), in), "port80")
	if got.Code != "port80_closed" || got.CertFix == nil || got.CertFix.Command != "ufw allow 80/tcp" {
		t.Fatalf("closed: %+v", got)
	}
	c.Fetch = func(context.Context, string) (int, string, string, error) { return 404, "<html>", "nginx/1.24", nil }
	if got := find(t, c.Run(context.Background(), in), "port80"); got.Code != "port80_foreign" || got.Params["server"] != "nginx/1.24" {
		t.Fatalf("another server: %+v", got)
	}

	held, _ := net.Listen("tcp", "127.0.0.1:0")
	defer held.Close()
	busy := acmechallenge.New(held.Addr().String())
	in.Challenge = busy
	if got := find(t, c.Run(context.Background(), in), "port80"); got.Code != "port80_busy" || got.CertFix == nil || got.CertFix.Action != FixCopy {
		t.Fatalf("busy: %+v", got)
	}
}

func TestNodeCert(t *testing.T) {
	until := time.Now().Add(80 * 24 * time.Hour)
	for _, c := range []struct {
		name   string
		node   Node
		status string
		code   string
		fix    string
	}{
		{"public", Node{ID: 2, Kind: "acme", ACME: &acme.NodeStatus{CA: "letsencrypt", NotAfter: &until}}, OK, "node_acme_ok", ""},
		{"old node", Node{ID: 2, Kind: "self-signed", Pinned: true, ACME: &acme.NodeStatus{Error: acme.CodeNodeOutdated}}, Warn, "node_outdated", FixUpdateNode},
		{"firewall", Node{ID: 2, Kind: "self-signed", Pinned: true, ACME: &acme.NodeStatus{Error: acme.CodePort80Timeout}}, Fail, "node_acme_error", FixCopy},
		{"renewal failed", Node{ID: 2, Kind: "acme", ACME: &acme.NodeStatus{Error: acme.CodeRateLimited}}, Warn, "node_acme_error", FixRenewNode},
		{"not yet", Node{ID: 2, Kind: "self-signed", Pinned: true}, Warn, "node_self_signed", FixRenewNode},
		{"own", Node{ID: 2, Kind: "custom"}, OK, "node_own", ""},
		{"panel's own node", Node{ID: 1, Local: true, Kind: "panel"}, OK, "node_panel", ""},
	} {
		got := nodeCert(c.node, acme.Status{})
		fix := ""
		if got.CertFix != nil {
			fix = got.CertFix.Action
		}
		if got.Status != c.status || got.Code != c.code || fix != c.fix {
			t.Errorf("%s: %s %s fix %q", c.name, got.Status, got.Code, fix)
		}
	}
}

// A node's TLS port: the certificate it should serve, a pinned one, or something else.
func TestNodePort(t *testing.T) {
	p := newPKI(t)
	good := p.leaf(t, "nl.example.com", time.Now().Add(60*24*time.Hour), false)
	port := serve(t, good)
	c := checker(p, fakeDNS{})
	n := Node{ID: 3, Name: "NL", Host: "nl.example.com", SNI: "nl.example.com", Kind: "acme", Expected: good.Leaf, Ports: []Port{{Inbound: "anytls", Port: port}}}
	r := c.Run(context.Background(), Input{Nodes: []Node{n}})
	if got := find(t, r, "node_port"); got.Code != "node_port_ok" || got.NodeID != 3 {
		t.Fatalf("ok: %+v", got)
	}
	n.Expected = p.leaf(t, "nl.example.com", time.Now().Add(60*24*time.Hour), false).Leaf
	if got := find(t, c.Run(context.Background(), Input{Nodes: []Node{n}}), "node_port"); got.Code != "node_port_other" {
		t.Fatalf("other: %+v", got)
	}
}

// The worst comes first.
func TestOrder(t *testing.T) {
	cs := []CertCheck{{ID: "dns", Status: OK}, {ID: "port80", Status: Fail}, {ID: "served", Status: OK}, {ID: "acme", Status: Warn}}
	sortChecks(cs)
	if cs[0].ID != "port80" || cs[1].ID != "acme" || cs[2].ID != "served" || cs[3].ID != "dns" {
		t.Fatalf("%+v", cs)
	}
}
