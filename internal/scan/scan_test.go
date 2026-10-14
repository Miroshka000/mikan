package scan

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func server(t *testing.T, cfg func(*tls.Config)) string {
	t.Helper()
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	s.EnableHTTP2 = true
	s.TLS = &tls.Config{}
	if cfg != nil {
		cfg(s.TLS)
	}
	s.StartTLS()
	t.Cleanup(s.Close)
	return strings.TrimPrefix(s.URL, "https://")
}

func TestCheckReportsWhatTheTargetSupports(t *testing.T) {
	ctx := context.Background()
	// httptest's certificate is self-signed for example.com: everything but trust passes.
	r := Check(ctx, server(t, nil), "example.com", Options{Any: true})
	if !r.TLS13 || !r.H2 || !r.X25519 || r.CertValid || r.OK || r.RTTms < 0 || r.Error != "" {
		t.Fatalf("modern server: %+v", r)
	}
	r = Check(ctx, server(t, func(c *tls.Config) { c.MaxVersion = tls.VersionTLS12 }), "example.com", Options{Any: true})
	if r.TLS13 || r.OK {
		t.Fatalf("TLS 1.2 only: %+v", r)
	}
	r = Check(ctx, server(t, func(c *tls.Config) { c.CurvePreferences = []tls.CurveID{tls.CurveP256} }), "example.com", Options{Any: true})
	if r.X25519 || !r.TLS13 || r.OK {
		t.Fatalf("no X25519: %+v", r)
	}
}

func TestCheckNeedsNameForIP(t *testing.T) {
	if r := Check(context.Background(), "192.0.2.1:443", "", Options{}); r.Error != "sni_required" {
		t.Fatalf("an IP dest needs an explicit SNI: %+v", r)
	}
}

// A wide scan goes through the other /24s of the /16, the nearest first, inside it.
func TestWideBlocks(t *testing.T) {
	got := wideBlocks(netip.MustParseAddr("203.0.1.7"))
	if len(got) != 255 {
		t.Fatalf("%d blocks", len(got))
	}
	want := []string{"203.0.2.0/24", "203.0.0.0/24", "203.0.3.0/24", "203.0.4.0/24"}
	for i, w := range want {
		if got[i].String() != w {
			t.Fatalf("block %d: %s, want %s", i, got[i], w)
		}
	}
	if got[254].String() != "203.0.255.0/24" {
		t.Fatalf("last %s", got[254])
	}
}
