package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mikan/internal/release"
)

// A manifest the release command makes passes the clients in the field; one they would
// refuse (or read differently) stops the release.
func TestLegacyContract(t *testing.T) {
	f := newFixture(t, changelog)
	if _, err := f.manifest(f.args("0.5.0.1")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(f.dir, "release", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	good := string(data)
	check := func(text string) error { return legacyCheck([]byte(text), release.Sign([]byte(text), f.priv), f.pub) }
	if err := check(good); err != nil {
		t.Fatalf("the release command's own manifest: %v", err)
	}
	sum := strings.Repeat("0", 64)
	if _, err := f.manifest(f.args("0.5.0.1", "-min-installer", "0.5.0.0")); err != nil {
		t.Fatal(err)
	}
	withMin, _ := os.ReadFile(filepath.Join(f.dir, "release", "manifest.json"))
	for name, text := range map[string]string{
		"a minimum installer":  string(withMin),
		"three numbers":        strings.Replace(good, `"0.5.0.1"`, `"0.6.0"`, 1),
		"a pre-release":        strings.Replace(good, `"0.5.0.1"`, `"0.5.0.2-rc.1"`, 1),
		"a field it never had": strings.Replace(good, `"version"`, `"channel": "stable", "rollout": {"x": [1, 2]}, "version"`, 1),
		"a field in an asset":  strings.Replace(good, `"sha256"`, `"size": 12, "sha256"`, 1),
		"another image name":   strings.Replace(good, `ghcr.io/o0hhs/mikan"`, `ghcr.io/o0hhs/tools/mikan-2"`, 1),
		"no notes":             strings.Replace(good, `"notes"`, `"old_notes"`, 1),
		"an extra arch":        strings.Replace(good, `"installer": {`, `"installer": {"riscv64": {"url": "https://x/y", "sha256": "`+sum+`"},`, 1),
	} {
		if err := check(text); err != nil {
			t.Errorf("%s: refused: %v", name, err)
		}
	}
	for name, text := range map[string]string{
		"five numbers":                     strings.Replace(good, `"0.5.0.1"`, `"0.5.0.1.2"`, 1),
		"a v in the version":               strings.Replace(good, `"0.5.0.1"`, `"v0.5.0.1"`, 1),
		"a number too big":                 strings.Replace(good, `"0.5.0.1"`, `"0.5.0.18446744073709551616"`, 1),
		"an empty pre-release part":        strings.Replace(good, `"0.5.0.1"`, `"0.5.0.1-rc..1"`, 1),
		"a version that is a number":       strings.Replace(good, `"0.5.0.1"`, `501`, 1),
		"no version":                       strings.Replace(good, `"version"`, `"release"`, 1),
		"a version in capitals":            strings.Replace(good, `"version"`, `"Version": "9.9.9", "version"`, 1),
		"a version twice":                  strings.Replace(good, `"version"`, `"version": "0.5.0.1", "version"`, 1),
		"a minimum installer that is null": strings.Replace(good, `"version"`, `"min_installer": null, "version"`, 1),
		"a minimum installer newer":        strings.Replace(good, `"version"`, `"min_installer": "0.5.0.2", "version"`, 1),
		"a bad minimum installer":          strings.Replace(good, `"version"`, `"min_installer": "0.5", "version"`, 1),
		"no date":                          strings.Replace(good, `"published"`, `"released"`, 1),
		"a date that is not RFC 3339":      strings.Replace(good, `"published": "`, `"published": "yesterday`, 1),
		"a date that is a number":          strings.Replace(good, `"published": "`, `"published": 1, "released": "`, 1),
		"an image elsewhere":               strings.Replace(good, `ghcr.io/o0hhs/mikan"`, `ghcr.io/someone/mikan"`, 1),
		"an image on docker hub":           strings.Replace(good, `ghcr.io/o0hhs/mikan"`, `docker.io/o0hhs/mikan"`, 1),
		"an image in capitals":             strings.Replace(good, `ghcr.io/o0hhs/mikan"`, `ghcr.io/o0hhs/Mikan"`, 1),
		"a short digest":                   strings.Replace(good, `"sha256:aaaa`, `"sha256:aa`, 1),
		"no installers":                    strings.Replace(good, `"installer": {`, `"installer": null, "old_installer": {`, 1),
		"installers that are a list":       strings.Replace(good, `"installer": {`, `"installer": [], "old_installer": {`, 1),
		"an installer without a url":       strings.Replace(good, `"url"`, `"link"`, 1),
		"an installer over http":           strings.Replace(good, `"url": "https://`, `"url": "http://`, 1),
		"an installer with a bad hash":     strings.Replace(good, `"sha256": "`, `"sha256": "x`, 1),
		"an installer with a null hash":    strings.Replace(good, `"installer": {`, `"installer": {"riscv64": {"url": "https://x/y", "sha256": null},`, 1),
		"no aarch64":                       strings.Replace(good, `"aarch64"`, `"arm"`, 1),
		"notes that are null":              strings.Replace(good, `"notes": {`, `"notes": null, "old_notes": {`, 1),
		"a note that is not text":          strings.Replace(good, `"notes": {`, `"notes": {"de": 1,`, 1),
		"a note that is null":              strings.Replace(good, `"notes": {`, `"notes": {"de": null,`, 1),
		"not an object":                    "[" + good + "]",
		"data after it":                    good + "{}",
		"not UTF-8":                        strings.Replace(good, `"notes": {`, "\"notes\": {\"de\": \"\xff\",", 1),
		"nested too deep":                  strings.Replace(good, `"version"`, `"x": `+strings.Repeat("[", 120)+strings.Repeat("]", 120)+`, "version"`, 1),
		"too large":                        strings.Replace(good, `"version"`, `"x": "`+strings.Repeat("a", 1<<20)+`", "version"`, 1),
	} {
		if text == good {
			t.Errorf("%s: the case does not change the manifest", name)
			continue
		}
		if err := check(text); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	other, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	if err := legacyCheck([]byte(good), release.Sign([]byte(good), otherPriv), f.pub); err == nil {
		t.Error("another key's signature accepted")
	}
	if err := legacyCheck([]byte(good), release.Sign([]byte(good), f.priv), other); err == nil {
		t.Error("a signature checked with another key accepted")
	}
	if err := legacyCheck([]byte(good), "not base64!", f.pub); err == nil {
		t.Error("a garbage signature accepted")
	}
}

// The command reads the manifest and its signature next to it and checks them with the
// release key the clients carry: a manifest signed by a test key is refused.
func TestLegacyCheckFile(t *testing.T) {
	f := newFixture(t, changelog)
	if _, err := f.manifest(f.args("0.5.0.1")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.dir, "release", "manifest.json")
	if err := legacyCheckFile([]string{path}); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("a test key's manifest: %v", err)
	}
	if err := legacyCheckFile(nil); err == nil {
		t.Fatal("no file")
	}
}

// The frozen version rules order versions as the released clients did: the shared table
// (testdata/versions.json) was made from them.
func TestLegacyVersionsMatchTheTable(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"0.5.0.1", "0.5.0.0", 1}, {"0.5.0.0", "0.4.5", 1}, {"0.4.5", "0.4.5.0", 0}, {"0.5.0.1-rc.1", "0.5.0.1", -1},
		{"0.5.0.1-rc.10", "0.5.0.1-rc.9", 1}, {"", "0.0.1", -1}, {"dev", "dev", 0}, {"0.5.0.1-rc.1", "0.5.0.1-1", 1},
	} {
		if got := legacyCompare(c.a, c.b); got != c.want {
			t.Errorf("legacyCompare(%q, %q) = %d", c.a, c.b, got)
		}
		if got := legacyCompare(c.a, c.b) > 0; got != release.Newer(c.a, c.b) {
			t.Errorf("%q, %q: the frozen and the current rules disagree", c.a, c.b)
		}
	}
}
