package app

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mikan/internal/panel/acme"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/store/storetest"
	"mikan/internal/panel/tlscert"
)

// pairFor writes a certificate for name (a domain or an IP) into dir as the ACME manager
// keeps one: cert.pem and key.pem.
func pairFor(t *testing.T, dir, name string, until time.Time) {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name},
		NotBefore: until.Add(-90 * 24 * time.Hour), NotAfter: until}
	if ip := net.ParseIP(name); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{name}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	kd, _ := x509.MarshalPKCS8PrivateKey(k)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "key.pem"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kd}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cert.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The certificate of a node's protocols on TLS: its own first, then the public one the
// panel ordered for it, the panel's for its own node (an IP's too), self-signed and pinned
// last. Only the self-signed one and an own untrusted one carry a pin.
func TestNodeCertificateOrder(t *testing.T) {
	ctx := t.Context()
	t.Setenv("MIKAN_ACME_DIRECTORY", "http://127.0.0.1:1/directory")
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	set := settings.New(st.Q)
	if err := settings.Set(ctx, set, settings.KeyPublicHost, "198.51.100.7"); err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	self, err := tlscert.LoadOrCreateSelfSigned(filepath.Join(data, "tls"), "198.51.100.7", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	holder := &tlscert.Holder{}
	holder.Set(self)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	panel := acme.New(data, holder, self, set, log, time.Now)
	panel.Load(ctx)
	orders := panel.Nodes(nil)
	own := tlscert.NewNodeStore(filepath.Join(data, "tls", "custom-nodes"), time.Now)
	x := NewNodeTLS(data, own, panel, orders, set, log, time.Now)

	remote := db.Node{ID: 2, Address: "203.0.113.2:9443", PublicHost: "203.0.113.2", Domain: "nl.example.com"}
	pick := func(n db.Node) TLSChoice {
		t.Helper()
		c, err := x.Pick(n)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	if c := pick(remote); c.Kind != "self-signed" || c.Pin == "" {
		t.Fatalf("nothing yet: %s pin %q", c.Kind, c.Pin)
	}
	acmeDir := filepath.Join(data, "tls", "acme-nodes", "2")
	pairFor(t, acmeDir, "nl.example.com", time.Now().Add(60*24*time.Hour))
	if c := pick(remote); c.Kind != "acme" || c.Pin != "" || c.CA != acme.CALetsEncrypt {
		t.Fatalf("ordered: %s pin %q ca %q", c.Kind, c.Pin, c.CA)
	}
	certPEM, keyPEM := testCert(t, "nl.example.com", time.Now().Add(30*24*time.Hour))
	if _, err := own.Set(2, []byte(certPEM), []byte(keyPEM)); err != nil {
		t.Fatal(err)
	}
	if c := pick(remote); c.Kind != "custom" || c.Pin == "" {
		t.Fatalf("own (untrusted): %s pin %q", c.Kind, c.Pin)
	}
	if err := own.Clear(2); err != nil {
		t.Fatal(err)
	}
	// One that has expired is not served: the pinned self-signed one is.
	pairFor(t, acmeDir, "nl.example.com", time.Now().Add(-time.Hour))
	if c := pick(remote); c.Kind != "self-signed" || c.Pin == "" {
		t.Fatalf("expired: %s pin %q", c.Kind, c.Pin)
	}

	// The panel's own node on an IP: the panel's six-day IP certificate, no pin.
	local := db.Node{ID: 1}
	if c := pick(local); c.Kind != "self-signed" || c.Pin == "" {
		t.Fatalf("local, nothing public: %s pin %q", c.Kind, c.Pin)
	}
	pairFor(t, filepath.Join(data, "tls", "acme"), "198.51.100.7", time.Now().Add(6*24*time.Hour))
	panel.Load(ctx)
	if c := pick(local); c.Kind != "panel" || c.Pin != "" {
		t.Fatalf("local with the panel's IP certificate: %s pin %q (%+v)", c.Kind, c.Pin, panel.Status())
	}
}
