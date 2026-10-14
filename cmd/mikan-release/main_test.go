package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mikan/internal/release"
)

// fixture is a release directory: a signing key in RELEASE_SIGNING_KEY, a changelog with
// both languages and an installer binary for each architecture.
type fixture struct {
	t    *testing.T
	pub  ed25519.PublicKey
	priv ed25519.PrivateKey
	dir  string
}

func newFixture(t *testing.T, changelog string) *fixture {
	t.Helper()
	pub, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("RELEASE_SIGNING_KEY", string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})))
	f := &fixture{t: t, pub: pub, priv: private, dir: t.TempDir()}
	f.write("CHANGELOG.md", changelog)
	f.write("mikan-x86_64", "installer x86_64")
	f.write("mikan-aarch64", "installer aarch64")
	return f
}

func (f *fixture) write(name, content string) string {
	f.t.Helper()
	path := filepath.Join(f.dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		f.t.Fatal(err)
	}
	return path
}

// args are a complete manifest command for version; extra flags go after them.
func (f *fixture) args(version string, extra ...string) []string {
	return append([]string{"-version", version, "-image", "ghcr.io/o0hhs/mikan", "-digest", "sha256:" + strings.Repeat("a", 64),
		"-changelog", filepath.Join(f.dir, "CHANGELOG.md"), "-out", filepath.Join(f.dir, "release"),
		"-asset", "x86_64=" + filepath.Join(f.dir, "mikan-x86_64"), "-asset", "aarch64=" + filepath.Join(f.dir, "mikan-aarch64")}, extra...)
}

// manifest runs the command and reads back what it signed.
func (f *fixture) manifest(args []string) (release.Manifest, error) {
	f.t.Helper()
	if err := makeManifest(args); err != nil {
		return release.Manifest{}, err
	}
	out := filepath.Join(f.dir, "release")
	data, err := os.ReadFile(filepath.Join(out, "manifest.json"))
	if err != nil {
		f.t.Fatal(err)
	}
	sig, err := os.ReadFile(filepath.Join(out, "manifest.json.sig"))
	if err != nil {
		f.t.Fatal(err)
	}
	m, err := release.Parse(data, string(sig), f.pub)
	if err != nil {
		f.t.Fatalf("the signed manifest does not parse: %v", err)
	}
	return m, nil
}

const changelog = "## 0.5.0.1\n### en\n- patch\n\n### ru\n- заплатка\n\n## 0.4.4\n### en\n- old\n\n### ru\n- старое\n"

func TestManifestCarriesOnlyAnExplicitInstallerRequirement(t *testing.T) {
	f := newFixture(t, changelog)
	// Without the flag a release asks for no installer: a failed self-update must not
	// block a patch the old installer can apply.
	m, err := f.manifest(f.args("0.5.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != "0.5.0.1" || m.MinInstaller != "" || m.Notes["ru"] != "- заплатка" {
		t.Fatalf("unexpected manifest: %+v", m)
	}
	if m, err = f.manifest(f.args("0.5.0.1", "-min-installer", "0.5.0.0")); err != nil || m.MinInstaller != "0.5.0.0" {
		t.Fatalf("explicit installer requirement lost: %+v %v", m, err)
	}
	if err := makeManifest(f.args("0.5.0.1", "-min-installer", "0.5.0.1.2")); err == nil {
		t.Fatal("invalid installer version accepted")
	}
	if err := makeManifest(f.args("0.5.0.1", "-min-installer", "0.5.0.2")); err == nil {
		t.Fatal("installer newer than published binaries accepted")
	}
}

// The release workflow always passes -min-installer-file; the file is absent unless a
// release needs a newer installer.
func TestManifestReadsTheInstallerRequirementFromAFile(t *testing.T) {
	f := newFixture(t, changelog)
	file := filepath.Join(f.dir, "min-installer")
	m, err := f.manifest(f.args("0.5.0.1", "-min-installer-file", file))
	if err != nil || m.MinInstaller != "" {
		t.Fatalf("an absent file asks for an installer: %+v %v", m, err)
	}
	f.write("min-installer", "# 0.5.0.1 changes the compose file\n\n 0.5.0.0 \n")
	if m, err = f.manifest(f.args("0.5.0.1", "-min-installer-file", file)); err != nil || m.MinInstaller != "0.5.0.0" {
		t.Fatalf("the file's requirement lost: %+v %v", m, err)
	}
	if m, err = f.manifest(f.args("0.5.0.1", "-min-installer-file", file, "-min-installer", "0.5.0.0")); err != nil || m.MinInstaller != "0.5.0.0" {
		t.Fatalf("the same requirement twice: %+v %v", m, err)
	}
	if err := makeManifest(f.args("0.5.0.1", "-min-installer-file", file, "-min-installer", "0.4.5")); err == nil {
		t.Fatal("a flag and a file that disagree are accepted")
	}
	f.write("min-installer", "# only a comment\n")
	if m, err = f.manifest(f.args("0.5.0.1", "-min-installer-file", file)); err != nil || m.MinInstaller != "" {
		t.Fatalf("a file of comments asks for an installer: %+v %v", m, err)
	}
	f.write("min-installer", "0.5.0.0\n0.4.5\n")
	if err := makeManifest(f.args("0.5.0.1", "-min-installer-file", file)); err == nil {
		t.Fatal("two versions in the file are accepted")
	}
	f.write("min-installer", "latest\n")
	if err := makeManifest(f.args("0.5.0.1", "-min-installer-file", file)); err == nil {
		t.Fatal("a file without a version is accepted")
	}
}

// A release without an architecture's installer would strand those servers on the old
// one; notes missing a language would show the panel nothing in it.
func TestManifestRequiresEveryArchitectureAndLanguage(t *testing.T) {
	f := newFixture(t, changelog)
	base := f.args("0.5.0.1")
	without := func(drop string) []string {
		var out []string
		for i := 0; i < len(base); i++ {
			if base[i] == "-asset" && strings.HasPrefix(base[i+1], drop+"=") {
				i++
				continue
			}
			out = append(out, base[i])
		}
		return out
	}
	for _, arch := range []string{"x86_64", "aarch64"} {
		err := makeManifest(without(arch))
		if err == nil || !strings.Contains(err.Error(), "no installer for "+arch) {
			t.Errorf("a release without %s: %v", arch, err)
		}
	}
	if err := makeManifest(f.args("0.5.0.1", "-asset", "riscv64="+filepath.Join(f.dir, "mikan-x86_64"))); err == nil {
		t.Error("an unknown architecture is accepted")
	}
	if err := makeManifest(f.args("0.5.0.1", "-asset", "x86_64="+filepath.Join(f.dir, "mikan-aarch64"))); err == nil {
		t.Error("an architecture given twice is accepted")
	}
	for name, log := range map[string]string{
		"en": "## 0.5.0.1\n### ru\n- заплатка\n",
		"ru": "## 0.5.0.1\n### en\n- patch\n",
		"":   "## 0.5.0.0\n### en\n- other\n### ru\n- другое\n",
	} {
		f.write("CHANGELOG.md", log)
		err := makeManifest(base)
		if err == nil || (name != "" && !strings.Contains(err.Error(), `"`+name+`" notes`)) {
			t.Errorf("notes without %q: %v", name, err)
		}
	}
}

// A bridge manifest names installers published under another tag; by default they come
// from the release's own tag.
func TestManifestInstallerURLFollowsTheTag(t *testing.T) {
	f := newFixture(t, changelog)
	m, err := f.manifest(f.args("0.4.4"))
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Installer["x86_64"].URL; got != "https://github.com/o0hhs/mikanaddons/releases/download/v0.4.4/mikan-x86_64" {
		t.Fatal(got)
	}
	if got := m.Installer["aarch64"].URL; got != "https://github.com/o0hhs/mikanaddons/releases/download/v0.4.4/mikan-aarch64" {
		t.Fatal(got)
	}
	if m, err = f.manifest(f.args("0.4.4", "-tag", "v-bridge-0.5.0.0")); err != nil {
		t.Fatal(err)
	}
	if got := m.Installer["x86_64"].URL; got != "https://github.com/o0hhs/mikanaddons/releases/download/v-bridge-0.5.0.0/mikan-x86_64" {
		t.Fatal(got)
	}
}
