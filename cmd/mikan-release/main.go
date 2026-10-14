// mikan-release makes the signed manifest of a release and the signed release index (used
// by .github/workflows/release.yml), checks a manifest against what the clients in the
// field read (legacy.go), and makes the release signing key.
//
//	mikan-release keygen -out release-signing.pem
//	RELEASE_SIGNING_KEY="$(cat key.pem)" mikan-release manifest -version 0.3.9 \
//	    -image ghcr.io/o0hhs/mikan -digest sha256:… \
//	    -asset x86_64=dist/mikan-x86_64 -asset aarch64=dist/mikan-aarch64 \
//	    -min-installer-file .github/min-installer -out dist
//	mikan-release verify dist/manifest.json
//	mikan-release legacy-check dist/manifest.json
//	RELEASE_SIGNING_KEY="$(cat key.pem)" mikan-release index -version 0.5.0.1 \
//	    -from-file .github/upgrade-from -in updates/index.json -out dist
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"mikan/internal/release"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "mikan-release:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: mikan-release keygen|manifest|verify|legacy-check|index|goals …")
	}
	switch args[0] {
	case "keygen":
		return keygen(args[1:])
	case "manifest":
		return makeManifest(args[1:])
	case "verify":
		return verify(args[1:])
	case "legacy-check":
		return legacyCheckFile(args[1:])
	case "index":
		return makeIndex(args[1:])
	case "goals":
		return makeGoals(args[1:])
	}
	return fmt.Errorf("unknown command %q", args[0])
}

func keygen(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ContinueOnError)
	out := fs.String("out", "", "file for the private key (PKCS#8 PEM)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return errors.New("-out is required")
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := pem.Encode(f, &pem.Block{Type: "PRIVATE KEY", Bytes: der}); err != nil {
		return err
	}
	fmt.Println(base64.StdEncoding.EncodeToString(pub))
	return nil
}

// Every release carries the installer for each architecture the installer runs on
// (std::env::consts::ARCH) and the notes in each language the panel shows.
var (
	architectures = []string{"x86_64", "aarch64"}
	languages     = []string{"en", "ru"}
)

// readVersionFile reads a version from a file the release workflow passes on every
// release (the minimum installer, the lowest version to update from): absent or holding
// only comments, it says none.
func readVersionFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var version string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if version != "" {
			return "", fmt.Errorf("%s: more than one version", path)
		}
		version = line
	}
	return version, nil
}

type assets []string

func (a *assets) String() string     { return strings.Join(*a, ",") }
func (a *assets) Set(v string) error { *a = append(*a, v); return nil }

func makeManifest(args []string) error {
	fs := flag.NewFlagSet("manifest", flag.ContinueOnError)
	version := fs.String("version", "", "release version, e.g. 0.5.0.1")
	minInstaller := fs.String("min-installer", "", "minimum host installer version, only for a release that cannot run with an older one")
	minInstallerFile := fs.String("min-installer-file", "", "file with the minimum installer version (# comments allowed); a missing file asks for none")
	image := fs.String("image", "", "image repository, e.g. ghcr.io/o0hhs/mikan")
	digest := fs.String("digest", "", "sha256 digest of the pushed multi-arch image")
	changelog := fs.String("changelog", "CHANGELOG.md", "where the release notes are")
	out := fs.String("out", "dist", "output directory")
	tag := fs.String("tag", "", "release tag the installers are downloaded from (default v<version>)")
	var files assets
	fs.Var(&files, "asset", "arch=path of an installer binary (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	key, err := privateKey(os.Getenv("RELEASE_SIGNING_KEY"))
	if err != nil {
		return err
	}
	log, err := os.ReadFile(*changelog)
	if err != nil {
		return err
	}
	if *tag == "" {
		*tag = "v" + *version
	}
	if *minInstallerFile != "" {
		v, err := readVersionFile(*minInstallerFile)
		if err != nil {
			return err
		}
		if v != "" && *minInstaller != "" && v != *minInstaller {
			return fmt.Errorf("-min-installer %s and %s (%s) disagree", *minInstaller, *minInstallerFile, v)
		}
		if v != "" {
			*minInstaller = v
		}
	}
	m := release.Manifest{Version: *version, MinInstaller: *minInstaller, Published: time.Now().UTC().Truncate(time.Second), Image: *image, Digest: *digest,
		Installer: map[string]release.Asset{}, Notes: release.NotesFor(log, *version)}
	for _, lang := range languages {
		if m.Notes[lang] == "" {
			return fmt.Errorf("%s has no %q notes for %s (want \"## %s\" with \"### en\" and \"### ru\")", *changelog, lang, *version, *version)
		}
	}
	for _, a := range files {
		arch, path, ok := strings.Cut(a, "=")
		if !ok {
			return fmt.Errorf("-asset %q: want arch=path", a)
		}
		if !slices.Contains(architectures, arch) {
			return fmt.Errorf("-asset %q: unknown architecture %q, want one of %s", a, arch, strings.Join(architectures, ", "))
		}
		if _, dup := m.Installer[arch]; dup {
			return fmt.Errorf("-asset %q: %s given twice", a, arch)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		m.Installer[arch] = release.Asset{
			URL:    fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", release.Repo, *tag, filepath.Base(path)),
			SHA256: hex.EncodeToString(sum[:]),
		}
	}
	// A server updates its installer only from the manifest: a release without one
	// architecture would leave those servers on the old installer.
	for _, arch := range architectures {
		if _, ok := m.Installer[arch]; !ok {
			return fmt.Errorf("no installer for %s: pass -asset %s=path", arch, arch)
		}
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	sig := release.Sign(data, key)
	// The manifest must pass the checks the panel and the installer make.
	if _, err := release.Parse(data, sig, key.Public().(ed25519.PublicKey)); err != nil {
		return err
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	notes := m.Notes["en"]
	if ru := m.Notes["ru"]; ru != "" {
		notes += "\n\n<details><summary>Русский</summary>\n\n" + ru + "\n\n</details>"
	}
	for name, content := range map[string][]byte{"manifest.json": data, "manifest.json.sig": []byte(sig + "\n"), "notes.md": []byte(notes + "\n")} {
		if err := os.WriteFile(filepath.Join(*out, name), content, 0o644); err != nil {
			return err
		}
	}
	fmt.Printf("manifest %s: %s\n", m.Version, m.Ref())
	return nil
}

func verify(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: mikan-release verify manifest.json")
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(args[0] + ".sig")
	if err != nil {
		return err
	}
	pub, err := release.Key(release.PublicKey)
	if err != nil {
		return err
	}
	m, err := release.Parse(data, string(sig), pub)
	if err != nil {
		return err
	}
	fmt.Printf("ok: %s %s\n", m.Version, m.Ref())
	return nil
}

func privateKey(pemText string) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		return nil, errors.New("RELEASE_SIGNING_KEY: no PEM key")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("RELEASE_SIGNING_KEY: %w", err)
	}
	priv, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("RELEASE_SIGNING_KEY: not an Ed25519 key")
	}
	return priv, nil
}
