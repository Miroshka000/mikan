package nodehello

import (
	"context"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"mikan/internal/nodetls"
)

func TestStatusFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := Read(dir); err == nil {
		t.Fatal("no file yet")
	}
	at := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	s := Status{Result: Result{Code: "timeout", Params: map[string]string{"port": "31234"}, SeenIP: "203.0.113.7"}, At: at, Started: at.Add(-time.Minute), Attempt: 3, Final: true}
	if err := Write(dir, s); err != nil {
		t.Fatal(err)
	}
	got, err := Read(dir)
	if err != nil || got.Code != "timeout" || got.Params["port"] != "31234" || !got.At.Equal(at) || !got.Final || got.Attempt != 3 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestFinal(t *testing.T) {
	for r, want := range map[*Result]bool{
		{OK: true}:               true,
		{Code: "timeout"}:        false,
		{Code: "refused"}:        false,
		{Code: PanelUnreachable}: false,
		{Code: "pin_mismatch"}:   true,
		{Code: PanelRejected}:    true,
		{Code: NoPanelURL}:       true,
		{Code: PanelUnverified}:  true,
		{Code: PanelUntrusted}:   true,
	} {
		if Final(*r) != want {
			t.Errorf("%+v: %v", *r, !want)
		}
	}
}

// A panel still on its self-signed certificate is not asked: the hello checks the
// certificate like any client and says why it did not go, which is no failure of the node.
func TestHelloToAnUntrustedPanel(t *testing.T) {
	panel := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the hello went past an untrusted certificate")
	}))
	defer panel.Close()
	now := time.Now()
	pc, _ := nodetls.Generate("mikan-panel", x509.ExtKeyUsageClientAuth, now)
	nc, _ := nodetls.Generate("node-2.mikan", x509.ExtKeyUsageServerAuth, now)
	pin, _ := nodetls.Fingerprint(pc.CertPEM)
	key := nodetls.Key{Port: 40123, PanelPin: pin, CertPEM: nc.CertPEM, KeyPEM: nc.KeyPEM, PanelURL: panel.URL}
	r := Send(context.Background(), key, Client(), now)
	if r.Code != PanelUntrusted || r.Params["url"] != panel.URL || !Final(r) {
		t.Fatalf("%+v", r)
	}
}
