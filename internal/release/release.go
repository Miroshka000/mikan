// Package release is the manifest every mikan release publishes next to its files: the
// version, the image digest in GitHub Packages, the installer checksums and what changed.
// The panel and the installer trust a manifest only with a valid signature from the
// release key, whose public half is PublicKey.
package release

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// PublicKey is the public half of the release signing key (raw Ed25519, base64). The
// private half lives only in the RELEASE_SIGNING_KEY secret of the release workflow and
// with the maintainer. It is this fork's own key: releases of o0hhs/mikanaddons are
// signed with it, and a manifest signed with the upstream key is refused here.
const PublicKey = "H+GnL17JW6e7qw4qsYOC2YNMFPLp1mTjJmUqops3z7o="

// Repo is where this fork publishes its releases.
const Repo = "o0hhs/mikanaddons"

// InstallCommand installs mikan from the latest release; run on the server.
const InstallCommand = "curl -fsSL https://github.com/" + Repo + "/releases/latest/download/install.sh | sudo bash"

// JoinCommand installs a node of an existing panel with the join key the panel issued.
func JoinCommand(key string) string { return InstallCommand + " -s -- --join " + key }

// LatestURL is the manifest of the newest release; its signature is LatestURL + ".sig".
const LatestURL = "https://github.com/" + Repo + "/releases/latest/download/manifest.json"

type Manifest struct {
	Version      string            `json:"version"`
	MinInstaller string            `json:"min_installer,omitempty"`
	Published    time.Time         `json:"published"`
	Image        string            `json:"image" doc:"Образ в GitHub Packages, например ghcr.io/o0hhs/mikan"`
	Digest       string            `json:"digest" doc:"sha256 multi-arch образа"`
	Installer    map[string]Asset  `json:"installer" doc:"Установщик по архитектуре: x86_64, aarch64"`
	Notes        map[string]string `json:"notes" doc:"Что изменилось, markdown по языкам: en, ru"`
}

type Asset struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

// Ref is the image pinned to the released digest.
func (m Manifest) Ref() string { return m.Image + "@" + m.Digest }

var (
	ErrSignature = errors.New("release: bad signature")
	versionRe    = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)(?:\.(\d+))?(?:-([0-9A-Za-z.-]+))?$`)
	digestRe     = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	sha256Re     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Key decodes a raw Ed25519 public key in base64.
func Key(b64 string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, errors.New("release: bad public key")
	}
	return raw, nil
}

// Sign returns the base64 signature of data.
func Sign(data []byte, key ed25519.PrivateKey) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(key, data))
}

// Parse checks the signature over the manifest's exact bytes, then decodes and checks it.
func Parse(data []byte, sig string, pub ed25519.PublicKey) (Manifest, error) {
	var m Manifest
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sig))
	if err != nil || len(pub) != ed25519.PublicKeySize || !ed25519.Verify(pub, data, raw) {
		return m, ErrSignature
	}
	// Unknown fields are ignored: the signature already vouches for every byte, and a
	// manifest that gains a field must still be read by the panels already out there.
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("release: manifest: %w", err)
	}
	if versionParts(m.Version) == nil {
		return m, fmt.Errorf("release: version %q", m.Version)
	}
	if m.MinInstaller != "" && versionParts(m.MinInstaller) == nil {
		return m, fmt.Errorf("release: minimum installer version %q", m.MinInstaller)
	}
	if Newer(m.MinInstaller, m.Version) {
		return m, errors.New("release: minimum installer is newer than the release")
	}
	if !digestRe.MatchString(m.Digest) || !validImage(m.Image) {
		return m, fmt.Errorf("release: image %s@%s", m.Image, m.Digest)
	}
	for arch, a := range m.Installer {
		if !sha256Re.MatchString(a.SHA256) || !strings.HasPrefix(a.URL, "https://") {
			return m, fmt.Errorf("release: installer for %s", arch)
		}
	}
	return m, nil
}

// imagePrefix is the project's own namespace on GitHub Packages, the only place a release
// image comes from: ghcr.io/<owner of Repo in lowercase>/.
var imagePrefix = "ghcr.io/" + strings.ToLower(strings.SplitN(Repo, "/", 2)[0]) + "/"

// validImage checks the image as the installer does (installer/src/release.rs): the value
// goes into the host's .env and compose file, so it holds an image name's characters only.
func validImage(image string) bool {
	name, ok := strings.CutPrefix(image, imagePrefix)
	return ok && name != "" && !strings.Contains(name, "..") &&
		strings.Trim(name, "abcdefghijklmnopqrstuvwxyz0123456789._/-") == ""
}

// MaxVersionLen is the longest version a request names: real ones are a dozen characters,
// and what a node writes to its host's disk stays small.
const MaxVersionLen = 64

// Valid says whether v is a release version (1.2.3 or 1.2.3.4, a pre-release after a
// hyphen allowed): not "dev", not a tag with a "v", not an address.
func Valid(v string) bool { return len(v) <= MaxVersionLen && versionParts(v) != nil }

// AtLeast says whether version a is b or later. A version that is not a release is never.
func AtLeast(a, b string) bool { return Valid(a) && Valid(b) && !Newer(b, a) }

// Newer says whether version a is later than b. A pre-release (1.2.3-rc.1) comes before
// its release; "dev" and other unparsable versions are older than everything.
func Newer(a, b string) bool {
	return compare(a, b) > 0
}

func compare(a, b string) int {
	pa, pb := versionParts(a), versionParts(b)
	switch {
	case pa == nil && pb == nil:
		return 0
	case pa == nil:
		return -1
	case pb == nil:
		return 1
	}
	for i := 1; i <= 4; i++ {
		x, _ := strconv.ParseUint(pa[i], 10, 64)
		y, _ := strconv.ParseUint(pb[i], 10, 64)
		if x != y {
			if x > y {
				return 1
			}
			return -1
		}
	}
	switch {
	case pa[5] == pb[5]:
		return 0
	case pa[5] == "":
		return 1
	case pb[5] == "":
		return -1
	}
	return comparePrerelease(pa[5], pb[5])
}

func versionParts(v string) []string {
	p := versionRe.FindStringSubmatch(v)
	if p == nil {
		return nil
	}
	for i := 1; i <= 4; i++ {
		if p[i] != "" {
			if _, err := strconv.ParseUint(p[i], 10, 64); err != nil {
				return nil
			}
		}
	}
	if p[5] != "" {
		for _, s := range strings.Split(p[5], ".") {
			if s == "" {
				return nil
			}
		}
	}
	return p
}

func comparePrerelease(a, b string) int {
	x, y := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(x) && i < len(y); i++ {
		if x[i] == y[i] {
			continue
		}
		numeric := func(s string) bool { return strings.Trim(s, "0123456789") == "" }
		xn, yn := numeric(x[i]), numeric(y[i])
		if xn != yn {
			if xn {
				return -1
			}
			return 1
		}
		if xn {
			xs, ys := strings.TrimLeft(x[i], "0"), strings.TrimLeft(y[i], "0")
			if len(xs) < len(ys) {
				return -1
			}
			if len(xs) > len(ys) {
				return 1
			}
			if c := strings.Compare(xs, ys); c != 0 {
				return c
			}
			continue
		}
		return strings.Compare(x[i], y[i])
	}
	if len(x) < len(y) {
		return -1
	}
	if len(x) > len(y) {
		return 1
	}
	return 0
}

// NotesFor is the notes of version: its own section, or for a pre-release without one
// (0.5.0.3-rc.1) the section of the release it leads to (0.5.0.3), so a beta needs no
// changelog entry of its own.
func NotesFor(changelog []byte, version string) map[string]string {
	notes := Notes(changelog, version)
	if base, _, pre := strings.Cut(version, "-"); pre && len(notes) == 0 {
		notes = Notes(changelog, base)
	}
	return notes
}

// Notes takes a version's section out of CHANGELOG.md:
//
//	## 0.3.9
//	### en
//	- …
//	### ru
//	- …
func Notes(changelog []byte, version string) map[string]string {
	notes := map[string]string{}
	var in bool
	var lang string
	var buf []string
	flush := func() {
		if lang != "" {
			if s := strings.TrimSpace(strings.Join(buf, "\n")); s != "" {
				notes[lang] = s
			}
		}
		buf = nil
	}
	for _, line := range strings.Split(strings.ReplaceAll(string(changelog), "\r\n", "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "## "):
			if in {
				flush()
				return notes
			}
			in = strings.TrimSpace(strings.TrimPrefix(line, "## ")) == version
		case in && strings.HasPrefix(line, "### "):
			flush()
			lang = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(line, "### ")))
		case in:
			buf = append(buf, line)
		}
	}
	flush()
	return notes
}
