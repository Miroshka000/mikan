package release

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func manifest(t *testing.T) []byte {
	t.Helper()
	data, err := json.Marshal(Manifest{
		Version: "0.3.9", Published: time.Unix(1_800_000_000, 0).UTC(),
		Image: "ghcr.io/o0hhs/mikan", Digest: "sha256:" + strings.Repeat("ab", 32),
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
	if m.Ref() != "ghcr.io/o0hhs/mikan@sha256:"+strings.Repeat("ab", 32) || m.Notes["ru"] != "- быстрее" {
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

// Only the project's own namespace on GitHub Packages, and installers with a real
// checksum behind https, as the installer checks (installer/src/release.rs).
func TestParseImageAndInstallers(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	accept := func(data []byte) error {
		_, err := Parse(data, Sign(data, priv), pub)
		return err
	}
	good := string(manifest(t))
	if err := accept([]byte(good)); err != nil {
		t.Fatal(err)
	}
	for _, image := range []string{
		"ghcr.io/o0hhs/mikan-node", "ghcr.io/o0hhs/tools/mikan_x.1",
	} {
		if err := accept([]byte(strings.Replace(good, "ghcr.io/o0hhs/mikan", image, 1))); err != nil {
			t.Errorf("%s: %v", image, err)
		}
	}
	for _, image := range []string{
		"docker.io/o0hhs/mikan", "ghcr.io/someone-else/mikan", "ghcr.io/Miroshka000/mikan", "ghcr.io/o0hhs/",
		"ghcr.io/o0hhs/../x", "ghcr.io/o0hhs/Mikan", "ghcr.io/o0hhs/mi kan", "ghcr.io/o0hhs/m$x",
		`ghcr.io/o0hhs/m\nx`, "ghcr.io/miroshka0001/mikan", "ghcr.io/mikan",
	} {
		if accept([]byte(strings.Replace(good, "ghcr.io/o0hhs/mikan", image, 1))) == nil {
			t.Errorf("image %q accepted", image)
		}
	}
	for name, bad := range map[string]string{
		"short hash":     strings.Replace(good, strings.Repeat("cd", 32), "cdcd", 1),
		"uppercase hash": strings.Replace(good, strings.Repeat("cd", 32), strings.Repeat("CD", 32), 1),
		"not hex":        strings.Replace(good, strings.Repeat("cd", 32), strings.Repeat("zz", 32), 1),
		"plain http":     strings.Replace(good, "https://example.com", "http://example.com", 1),
		"no url":         strings.Replace(good, "https://example.com/mikan-x86_64", "", 1),
	} {
		if accept([]byte(bad)) == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// testdata/versions.json is read by the installer's tests as well (installer/src/release.rs):
// both sides order and refuse versions the same way.
func TestVersionTable(t *testing.T) {
	data, err := os.ReadFile("../../testdata/versions.json")
	if err != nil {
		t.Fatal(err)
	}
	var table struct {
		Pairs []struct {
			A, B  string
			Newer bool
		}
		Valid, Invalid []string
	}
	if err := json.Unmarshal(data, &table); err != nil {
		t.Fatal(err)
	}
	if len(table.Pairs) == 0 || len(table.Valid) == 0 || len(table.Invalid) == 0 {
		t.Fatal("empty version table")
	}
	for _, c := range table.Pairs {
		if got := Newer(c.A, c.B); got != c.Newer {
			t.Errorf("Newer(%q, %q) = %v", c.A, c.B, got)
		}
		if c.Newer && Newer(c.B, c.A) {
			t.Errorf("Newer(%q, %q) and the other way round", c.A, c.B)
		}
	}
	for _, v := range table.Valid {
		if versionParts(v) == nil {
			t.Errorf("%q refused", v)
		}
	}
	for _, v := range table.Invalid {
		if versionParts(v) != nil {
			t.Errorf("%q accepted", v)
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

// Valid is what a request for a release may name: a version, nothing else of any length.
func TestValidAndAtLeast(t *testing.T) {
	for _, v := range []string{"0.5.0.2", "0.5.0", "1.2.3", "0.5.0.2-rc.1", "0.5.0.10"} {
		if !Valid(v) {
			t.Errorf("%q refused", v)
		}
	}
	for _, v := range []string{"", "dev", "latest", "v0.5.0.2", "0.5", "0.5.0.2.1", " 0.5.0.2", "0.5.0.2\n", "../0.5.0.2", "0.5.0.2-", "0.5.0.2-" + strings.Repeat("x", 80)} {
		if Valid(v) {
			t.Errorf("%q accepted", v)
		}
	}
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"0.5.0.2", "0.5.0.2", true}, {"0.5.0.3", "0.5.0.2", true}, {"0.5.1.0", "0.5.0.2", true}, {"0.6.0", "0.5.0.2", true},
		{"0.5.0.1", "0.5.0.2", false}, {"0.5.0", "0.5.0.2", false}, {"0.5.0.2-rc.1", "0.5.0.2", false}, {"0.5.0.0", "0.5.0", true},
		{"dev", "0.5.0.2", false}, {"0.5.0.2", "dev", false}, {"", "", false},
	} {
		if got := AtLeast(c.a, c.b); got != c.want {
			t.Errorf("AtLeast(%q, %q) = %v", c.a, c.b, got)
		}
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

// A beta takes its own section when there is one, else the section of the release it
// leads to; a stable release never borrows.
func TestNotesFor(t *testing.T) {
	log := []byte("# Changelog\n\n## 0.5.0.3-rc.2\n### en\n- rc2 only\n\n## 0.5.0.3\n### en\n- the release\n### ru\n- релиз\n")
	if n := NotesFor(log, "0.5.0.3-rc.1"); n["en"] != "- the release" || n["ru"] != "- релиз" {
		t.Fatalf("rc.1 borrows: %q", n)
	}
	if n := NotesFor(log, "0.5.0.3-rc.2"); n["en"] != "- rc2 only" {
		t.Fatalf("rc.2 has its own: %q", n)
	}
	if n := NotesFor(log, "0.5.0.4"); len(n) != 0 {
		t.Fatalf("a stable release without a section: %q", n)
	}
}
