package release

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func manifest(t *testing.T) []byte {
	t.Helper()
	data, err := json.Marshal(Manifest{
		Version: "0.3.9", Published: time.Unix(1_800_000_000, 0).UTC(),
		Image: "ghcr.io/miroshka000/mikan", Digest: "sha256:" + strings.Repeat("ab", 32),
		Installer: map[string]Asset{"x86_64": {URL: "https://example.com/mikan-x86_64", SHA256: strings.Repeat("cd", 32)}},
		Notes:     map[string]string{"en": "- faster", "ru": "- быстрее"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestParse(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	data := manifest(t)
	sig := Sign(data, priv)

	m, err := Parse(data, sig, pub)
	if err != nil {
		t.Fatal(err)
	}
	if m.Ref() != "ghcr.io/miroshka000/mikan@sha256:"+strings.Repeat("ab", 32) || m.Notes["ru"] != "- быстрее" {
		t.Fatalf("manifest: %+v", m)
	}

	tampered := []byte(strings.Replace(string(data), "0.3.9", "0.4.0", 1))
	if _, err := Parse(tampered, sig, pub); !errors.Is(err, ErrSignature) {
		t.Fatalf("a changed manifest is refused: %v", err)
	}
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := Parse(data, sig, other); !errors.Is(err, ErrSignature) {
		t.Fatalf("another key's signature is refused: %v", err)
	}
	if _, err := Parse(data, "not base64!", pub); !errors.Is(err, ErrSignature) {
		t.Fatalf("garbage signature: %v", err)
	}

	// Signed, but pointing somewhere else than GitHub Packages.
	bad := []byte(strings.Replace(string(data), "ghcr.io/", "docker.io/", 1))
	if _, err := Parse(bad, Sign(bad, priv), pub); err == nil {
		t.Fatal("an image outside ghcr.io is refused")
	}
}

// A later release may add a field to the manifest; the panels out there must still read it.
func TestParseIgnoresUnknownFields(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	data := []byte(strings.Replace(string(manifest(t)), `"version"`, `"channel":"beta","version"`, 1))
	m, err := Parse(data, Sign(data, priv), pub)
	if err != nil || m.Version != "0.3.9" {
		t.Fatalf("a manifest with a new field: %+v, %v", m, err)
	}
}

func TestEmbeddedKey(t *testing.T) {
	if _, err := Key(PublicKey); err != nil {
		t.Fatalf("the embedded release key: %v", err)
	}
}

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"0.3.10", "0.3.9", true},
		{"0.4.4.1", "0.4.4", true},
		{"0.4.4", "0.4.4.0", false},
		{"0.4.4.10", "0.4.4.9", true},
		{"0.4.5.0", "0.4.4.99", true},
		{"0.4.4.1", "0.4.4.1-rc.1", true},
		{"0.4.4.1-rc.10", "0.4.4.1-rc.9", true},
		{"0.4.4.1-99999999999999999999999", "0.4.4.1-9", true},
		{"0.4.4.1-rc..1", "0.4.4.0", false},
		{"0.4.4.18446744073709551616", "0.4.4.0", false},
		{"0.3.9", "0.3.10", false},
		{"0.4.0", "0.3.99", true},
		{"1.0.0", "0.9.9", true},
		{"0.3.9", "0.3.9", false},
		{"0.3.9", "0.3.9-rc.1", true},
		{"0.3.9-rc.2", "0.3.9-rc.1", true},
		{"0.3.9-rc.1", "0.3.8", true},
		{"0.3.9", "dev", true},
		{"dev", "0.3.9", false},
	} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

func TestFourComponentManifest(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	for _, version := range []string{"0.5.0.0", "0.5.0.1", "0.5.0.10-rc.1"} {
		data := []byte(strings.Replace(string(manifest(t)), "0.3.9", version, 1))
		if _, err := Parse(data, Sign(data, priv), pub); err != nil {
			t.Fatalf("version %s: %v", version, err)
		}
	}
	data := []byte(strings.Replace(string(manifest(t)), "0.3.9", "0.5.0.1.2", 1))
	if _, err := Parse(data, Sign(data, priv), pub); err == nil {
		t.Fatal("five components are refused")
	}
}

func TestNotes(t *testing.T) {
	log := []byte("# Changelog\n\n## 0.3.10\n### en\n- later\n\n## 0.3.9\n### en\n- one\n- two\n\n### ru\n- раз\n- два\n\n## 0.3.8\n### en\n- old\n")
	n := Notes(log, "0.3.9")
	if n["en"] != "- one\n- two" || n["ru"] != "- раз\n- два" || len(n) != 2 {
		t.Fatalf("notes: %q", n)
	}
	if n := Notes(log, "9.9.9"); len(n) != 0 {
		t.Fatalf("no section: %q", n)
	}
}
