package acme

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/storetest"
	"mikan/internal/panel/tlscert"
)

// The local node takes the panel's certificate only while clients trust it unpinned
// (GitHub issue #67): Let's Encrypt yes, the self-signed fallback no.
func TestPublic(t *testing.T) {
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
	data := t.TempDir()
	m := New(data, holder, fallback, set, slog.New(slog.NewTextHandler(io.Discard, nil)), func() time.Time { return now })
	m.ensure(ctx)
	if c := m.Public(); c != nil {
		t.Fatalf("the self-signed fallback is not public: %v", c.Leaf.Subject)
	}

	certPEM, keyPEM := ownCert(t, "vpn.example.com", now.Add(90*24*time.Hour))
	dir := filepath.Join(data, "tls", "acme")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cert.pem"), certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "key.pem"), keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	m.ensure(ctx)
	if c := m.Public(); c == nil || !tlscert.Covers(c.Leaf, "vpn.example.com") || m.Status().Kind != "acme" {
		t.Fatalf("Let's Encrypt: %+v", m.Status())
	}
}

// The local node gets the public certificate with its state, which the panel sends again
// only on a change it hears of; subscriptions drop the pin as soon as Public has one. So
// every change of Public is announced, or the node keeps a certificate the links no
// longer pin (0.5.0.3: Hysteria2, TUIC, AnyTLS and TrustTunnel failed their handshakes).
func TestPublicChangeIsAnnounced(t *testing.T) {
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
	data := t.TempDir()
	m := New(data, holder, fallback, set, slog.New(slog.NewTextHandler(io.Discard, nil)), func() time.Time { return now })
	calls := 0
	m.OnChange(func() { calls++ })

	m.Load(ctx)
	if calls != 0 || m.Public() != nil {
		t.Fatalf("nothing public yet: %d calls", calls)
	}
	store := func(until time.Time) {
		t.Helper()
		certPEM, keyPEM := ownCert(t, "vpn.example.com", until)
		dir := filepath.Join(data, "tls", "acme")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "cert.pem"), certPEM, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "key.pem"), keyPEM, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A certificate already on disk is served by Load, before any order and without one.
	store(now.Add(90 * 24 * time.Hour))
	m.Load(ctx)
	if m.Public() == nil || calls != 1 {
		t.Fatalf("stored certificate: public %v, %d calls", m.Public() != nil, calls)
	}
	m.Load(ctx)
	m.ensure(ctx)
	if calls != 1 {
		t.Fatalf("the same certificate was announced again: %d calls", calls)
	}
	// A renewal is a change.
	store(now.Add(89 * 24 * time.Hour))
	m.ensure(ctx)
	if calls != 2 {
		t.Fatalf("renewal: %d calls", calls)
	}
}
