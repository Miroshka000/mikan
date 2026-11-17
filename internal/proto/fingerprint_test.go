package proto

import (
	"strings"
	"testing"
)

// Node-certificate TLS dials through uTLS too; QUIC protocols have none.
func TestUsesFingerprint(t *testing.T) {
	for src, want := range map[string]bool{
		"type: trojan\nws-path: /ws\nmikan:\n  tls: node\n": true,
		"type: anytls\n":      true,
		"type: trusttunnel\n": true,
		"type: hysteria2\n":   false,
		"type: tuic\n":        false,
	} {
		if got := UsesFingerprint(mustParse(t, src)); got != want {
			t.Errorf("%q: %v, want %v", src, got, want)
		}
	}
}

func TestSetFingerprint(t *testing.T) {
	tpl := mustParse(t, "type: anytls\n")
	if err := SetFingerprint(tpl, "ios"); err != nil {
		t.Fatal(err)
	}
	if got := mustParse(t, Marshal(tpl)).Ext().Client.Fingerprint; got != "ios" {
		t.Fatalf("set: %q\n%s", got, Marshal(tpl))
	}
	for _, bad := range []string{"Chrome", "ios\nx: 1", "a,DIRECT", "chrome 120", strings.Repeat("a", 33)} {
		if code(SetFingerprint(tpl, bad)) != "config_fingerprint" {
			t.Fatalf("%q: malformed fingerprints must be refused", bad)
		}
	}
	// An own value of the right shape is the admin's call.
	if err := SetFingerprint(tpl, "randomizednoalpn"); err != nil {
		t.Fatal(err)
	}
	// Clearing drops the empty sections, other overrides stay.
	if err := SetFingerprint(tpl, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := tpl[extKey]; ok {
		t.Fatalf("empty mikan section left: %v", tpl)
	}
	tpl = mustParse(t, "type: anytls\nmikan:\n  client:\n    sni: cdn.example.com\n    fingerprint: qq\n")
	if err := SetFingerprint(tpl, ""); err != nil {
		t.Fatal(err)
	}
	if e := tpl.Ext(); e.Client.SNI != "cdn.example.com" || e.Client.Fingerprint != "" {
		t.Fatalf("clear: %+v", e)
	}
	if err := Validate(tpl, Options{}); err != nil {
		t.Fatal(err)
	}
}
