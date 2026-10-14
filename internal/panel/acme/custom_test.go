package acme

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"testing"
	"time"

	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/storetest"
	"mikan/internal/panel/tlscert"
)

func ownCert(t *testing.T, name string, until time.Time) (certPEM, keyPEM []byte) {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: until}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	kd, _ := x509.MarshalPKCS8PrivateKey(k)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kd})
}

// The admin's own certificate wins over Let's Encrypt while valid, must cover the panel's
// address, is noticed when its files change, and an expired one falls back and says so.
func TestCustomCertificate(t *testing.T) {
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
	clock := now
	m := New(t.TempDir(), holder, fallback, set, slog.New(slog.NewTextHandler(io.Discard, nil)), func() time.Time { return clock })
	served := func() *x509.Certificate {
		c, _ := holder.Get(&tls.ClientHelloInfo{})
		leaf, _ := x509.ParseCertificate(c.Certificate[0])
		return leaf
	}

	wrongCert, wrongKey := ownCert(t, "other.org", now.Add(90*24*time.Hour))
	if err := m.SetCustom(ctx, wrongCert, wrongKey); !errors.Is(err, tlscert.ErrWrongHost) {
		t.Fatalf("another host's certificate: %v", err)
	}
	certPEM, keyPEM := ownCert(t, "vpn.example.com", now.Add(90*24*time.Hour))
	if err := m.SetCustom(ctx, certPEM, keyPEM); err != nil {
		t.Fatal(err)
	}
	if s := m.Status(); s.Kind != "custom" || s.Error != "" || len(s.Names) != 1 || served().Subject.CommonName != "vpn.example.com" || served().Issuer.CommonName == "" {
		t.Fatalf("custom: %+v", s)
	}
	if m.customChanged() {
		t.Fatal("changed right after ensure")
	}
	// Expired: back to the self-signed one, and the status says why.
	clock = now.Add(100 * 24 * time.Hour)
	m.ensure(ctx)
	if s := m.Status(); s.Kind != "self-signed" || s.Error != "custom_expired" {
		t.Fatalf("expired: %+v", s)
	}
	clock = now
	if err := m.ClearCustom(); err != nil {
		t.Fatal(err)
	}
	if !m.customChanged() {
		t.Fatal("removing the files must count as a change")
	}
	m.ensure(ctx)
	if s := m.Status(); s.Kind != "self-signed" || s.Error == "custom_expired" {
		t.Fatalf("cleared: %+v", s)
	}
}

// HSTS goes out only while browsers trust what is served: a self-signed fallback, a custom
// certificate of a private CA or one for another host must stay clickable past a warning.
func TestTrusted(t *testing.T) {
	m := &Manager{}
	for _, c := range []struct {
		st   Status
		want bool
	}{
		{Status{Kind: "self-signed"}, false},
		{Status{Kind: "acme"}, true},
		{Status{Kind: "custom"}, false},
		{Status{Kind: "custom", Trusted: true}, true},
		{Status{Kind: "custom", Trusted: true, Error: "custom_wrong_host"}, false},
		{Status{Kind: "self-signed", Error: "custom_expired"}, false},
	} {
		m.status.Store(&c.st)
		if got := m.Trusted(); got != c.want {
			t.Fatalf("%+v: %v", c.st, got)
		}
	}
}
