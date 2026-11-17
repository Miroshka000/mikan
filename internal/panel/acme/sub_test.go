package acme

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"mikan/internal/acmechallenge"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/storetest"
	"mikan/internal/panel/tlscert"
)

// The subscription domain's certificate is its own: none while the domain is unset, the
// admin's own one for that domain only, kept apart from the panel's, and dropped when the
// domain goes.
func TestSubDomainCertificate(t *testing.T) {
	ctx := context.Background()
	t.Setenv("MIKAN_ACME_DIRECTORY", "http://127.0.0.1:1/directory") // the CA is unreachable here
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	set := settings.New(st.Q)
	if err := settings.Set(ctx, set, settings.KeyDomain, "vpn.example.com"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	holder := &tlscert.Holder{}
	m := NewSub(dir, holder, set, acmechallenge.New("127.0.0.1:0"), slog.New(slog.NewTextHandler(io.Discard, nil)), time.Now)
	m.Load(ctx)
	if s := m.Status(); s.Kind != "off" {
		t.Fatalf("no domain: %+v", s)
	}
	if _, err := holder.Get(&tls.ClientHelloInfo{}); err == nil {
		t.Fatal("a certificate without a domain")
	}

	if err := settings.Set(ctx, set, settings.KeySubDomain, "sub.example.com"); err != nil {
		t.Fatal(err)
	}
	panelCert, panelKey := ownCert(t, "vpn.example.com", time.Now().Add(90*24*time.Hour))
	if err := m.SetCustom(ctx, panelCert, panelKey); !errors.Is(err, tlscert.ErrWrongHost) {
		t.Fatalf("the panel's certificate for the subscription domain: %v", err)
	}
	certPEM, keyPEM := ownCert(t, "sub.example.com", time.Now().Add(90*24*time.Hour))
	if err := m.SetCustom(ctx, certPEM, keyPEM); err != nil {
		t.Fatal(err)
	}
	c, err := holder.Get(&tls.ClientHelloInfo{})
	if err != nil || c.Leaf.DNSNames[0] != "sub.example.com" || m.Status().Kind != "custom" {
		t.Fatalf("own certificate: %v %+v", err, m.Status())
	}
	// The panel's own certificate store is untouched.
	panel := New(dir, &tlscert.Holder{}, nil, set, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Now)
	panel.Load(ctx)
	if s := panel.Status(); s.Kind == "custom" {
		t.Fatalf("the subscription certificate leaked into the panel's: %+v", s)
	}

	if err := settings.Set(ctx, set, settings.KeySubDomain, ""); err != nil {
		t.Fatal(err)
	}
	m.Load(ctx)
	if _, err := holder.Get(&tls.ClientHelloInfo{}); err == nil || m.Status().Kind != "off" {
		t.Fatalf("the domain removed: %+v", m.Status())
	}
}

// An external edge owns ACME; the panel never orders its domain's certificate.
func TestExternalSubHasNoACMEIdentifier(t *testing.T) {
	ctx := t.Context()
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	set := settings.New(st.Q)
	if err := settings.Set(ctx, set, settings.KeySubDomain, "edge.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, set, settings.KeySubExternal, true); err != nil {
		t.Fatal(err)
	}
	m := NewSub(t.TempDir(), &tlscert.Holder{}, set, acmechallenge.New("127.0.0.1:0"), slog.New(slog.DiscardHandler), time.Now)
	if name, err := m.identifier(ctx); err != nil || name != "" {
		t.Fatalf("external ACME identifier=%q err=%v", name, err)
	}
}
