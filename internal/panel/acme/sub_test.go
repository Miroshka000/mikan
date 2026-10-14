package acme

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/storetest"
	"mikan/internal/panel/tlscert"
)

func leafWithNames(t *testing.T, names []string, until time.Time) (certPEM, keyPEM []byte) {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: names[0]}, DNSNames: names,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: until}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	kd, _ := x509.MarshalPKCS8PrivateKey(k)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kd})
}

// The subscription domain rides the panel's certificate as a second name: one on it is
// served with no order, a changed one asks for a new order while the panel keeps the old
// certificate. An address in the panel's identifier cannot carry a DNS name at all.
func TestSubscriptionDomainCertificate(t *testing.T) {
	ctx := context.Background()
	t.Setenv("MIKAN_ACME_DIRECTORY", "http://127.0.0.1:1/directory") // Let's Encrypt is unreachable here
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	set := settings.New(st.Q)
	if err := settings.Set(ctx, set, settings.KeyDomain, "vpn.example.com"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	fallback, err := tlscert.LoadOrCreateSelfSigned(t.TempDir(), "vpn.example.com", now)
	if err != nil {
		t.Fatal(err)
	}
	holder := &tlscert.Holder{}
	holder.Set(fallback)
	dataDir := t.TempDir()
	m := New(dataDir, holder, fallback, set, slog.New(slog.NewTextHandler(io.Discard, nil)), func() time.Time { return now })
	certPEM, keyPEM := leafWithNames(t, []string{"vpn.example.com", "sub.example.com"}, now.Add(90*24*time.Hour))
	acmeDir := filepath.Join(dataDir, "tls", "acme")
	if err := os.MkdirAll(acmeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(acmeDir, "cert.pem"), certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(acmeDir, "key.pem"), keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	served := func() *x509.Certificate {
		c, err := holder.Get(nil)
		if err != nil {
			t.Fatal(err)
		}
		leaf, _ := x509.ParseCertificate(c.Certificate[0])
		return leaf
	}

	if m.ensure(ctx) {
		t.Fatal("a certificate covering both names needs no order")
	}
	if s := m.Status(); s.Kind != "acme" || s.Error != "" {
		t.Fatalf("no subscription domain: %+v", s)
	}
	if err := settings.Set(ctx, set, settings.KeySubDomain, "sub.example.com"); err != nil {
		t.Fatal(err)
	}
	if m.ensure(ctx) {
		t.Fatal("a certificate covering the subscription domain needs no order")
	}
	if s := m.Status(); s.Kind != "acme" || s.Error != "" {
		t.Fatalf("with subscription domain: %+v", s)
	}
	// Another name: the order fails here, the panel's certificate is served meanwhile.
	if err := settings.Set(ctx, set, settings.KeySubDomain, "other.example.org"); err != nil {
		t.Fatal(err)
	}
	if !m.ensure(ctx) {
		t.Fatal("a certificate without the new name needs an order")
	}
	if s := m.Status(); s.Kind != "acme" || s.Error == "" {
		t.Fatalf("ordering failed: %+v", s)
	}
	if got := served(); !tlscert.Covers(got, "vpn.example.com") {
		t.Fatalf("the panel's certificate must keep serving: %v", got.DNSNames)
	}
	// An address cannot have the name beside it: the front's TLS, said in the status, and
	// no order anyone would refuse.
	if err := settings.Set(ctx, set, settings.KeyDomain, ""); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, set, settings.KeyPublicHost, "77.238.243.225"); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, set, settings.KeySubDomain, "sub.example.com"); err != nil {
		t.Fatal(err)
	}
	if m.ensure(ctx) {
		t.Fatal("no order is possible for an address beside a DNS name")
	}
	if s := m.Status(); s.Error != "sub_domain_no_tls" {
		t.Fatalf("address: %+v", s)
	}
	// The name off, the panel's certificate serves again as if nothing happened.
	if err := settings.Set(ctx, set, settings.KeySubDomain, ""); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, set, settings.KeyDomain, "vpn.example.com"); err != nil {
		t.Fatal(err)
	}
	if m.ensure(ctx) {
		t.Fatal("the panel's certificate covers its domain again")
	}
	if s := m.Status(); s.Kind != "acme" || s.Error != "" {
		t.Fatalf("cleared: %+v", s)
	}
}
