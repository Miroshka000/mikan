package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The legacy contract: how the releases already out in the field read the manifest at
// releases/latest/download/manifest.json, which they keep reading for as long as they
// run. Before a stable release is published (and so becomes "latest"), its manifest must
// pass every rule here, or the release stops before anything is published.
//
// The rules are copied from the released code, not from the current one:
//
//	git show v0.4.5:installer/src/release.rs        (installer 0.4.5: Manifest, parse, semver)
//	git show v0.5.0.0:installer/src/release.rs      (installer 0.5.0.0: the same, IMAGE_PREFIX)
//	git show v0.4.5:internal/release/release.go     (panel 0.4.5, branch release/0.4: Parse)
//	git show v0.5.0.0:internal/release/release.go   (panel 0.5.0.0: Parse, validImage)
//	git show v0.5.0.0:installer/src/update.rs       (self_update: the installer for the arch)
//
// Where they differ, the stricter one is the rule (serde wants the fields Go lets go
// missing, Go wants an RFC 3339 date where serde takes any string). This contract is
// frozen: it must never be loosened, whatever the current internal/release accepts. A
// new client may only add a contract of its own, for the day it is the oldest one out.

// legacyArchitectures are the installers every release carried when these clients were
// released: a client without its architecture's installer silently keeps its old command,
// and fails outright when the release asks for a newer one (min_installer).
var legacyArchitectures = []string{"x86_64", "aarch64"}

var (
	legacyVersionRe = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)(?:\.(\d+))?(?:-([0-9A-Za-z.-]+))?$`)
	legacyDigestRe  = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	legacySHA256Re  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

const (
	// net::get_opt(url, 1 << 20) and io.LimitReader(body, 1<<20): a longer manifest is cut.
	legacyMaxManifest = 1 << 20
	// net::get(sig, 4096) and io.LimitReader(body, 4096).
	legacyMaxSignature = 4096
	// serde_json's recursion limit, which unknown fields are parsed under too.
	legacyMaxDepth = 100
	// installer/src/release.rs IMAGE_PREFIX (0.5.0.0), the literal in valid_image (0.4.5).
	// The fork signs and serves its own releases from ghcr.io/o0hhs/, so this is the
	// namespace its own older clients would accept.
	legacyImagePrefix = "ghcr.io/o0hhs/"
)

// legacyCheck says whether every client still in the field takes data with sig as the
// newest release.
func legacyCheck(data []byte, sig string, pub ed25519.PublicKey) error {
	if len(data) > legacyMaxManifest || len(sig) > legacyMaxSignature {
		return errors.New("larger than the old clients download")
	}
	// verify(): base64 of the trimmed text, 64 bytes, the release key over the exact bytes.
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sig))
	if err != nil || len(raw) != ed25519.SignatureSize || !ed25519.Verify(pub, data, raw) {
		return errors.New("the signature does not verify with the release key")
	}
	// serde_json refuses what is not UTF-8, a repeated field and a nesting too deep.
	if !utf8.Valid(data) {
		return errors.New("not UTF-8")
	}
	if err := legacyStrictJSON(data); err != nil {
		return err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil || doc == nil {
		return errors.New("not a JSON object")
	}
	fields := []string{"version", "min_installer", "published", "image", "digest", "installer", "notes"}
	if err := legacyExactKeys(doc, fields); err != nil {
		return err
	}
	str := func(key string, required bool) (string, error) {
		v, ok := doc[key]
		if !ok {
			if required {
				return "", fmt.Errorf("%s is missing", key)
			}
			return "", nil
		}
		var s *string
		if json.Unmarshal(v, &s) != nil || s == nil {
			return "", fmt.Errorf("%s is not a string", key)
		}
		return *s, nil
	}
	version, err := str("version", true)
	if err != nil {
		return err
	}
	if legacyVersionParts(version) == nil {
		return fmt.Errorf("version %q: the old clients read three or four numbers and a pre-release", version)
	}
	minInstaller, err := str("min_installer", false)
	if err != nil {
		return err
	}
	if minInstaller != "" && legacyVersionParts(minInstaller) == nil {
		return fmt.Errorf("min_installer %q", minInstaller)
	}
	if legacyCompare(minInstaller, version) > 0 {
		return errors.New("min_installer is newer than the release")
	}
	published, err := str("published", true)
	if err != nil {
		return err
	}
	// The Go panels decode it into time.Time.
	var at time.Time
	if json.Unmarshal(doc["published"], &at) != nil {
		return fmt.Errorf("published %q is not an RFC 3339 time", published)
	}
	image, err := str("image", true)
	if err != nil {
		return err
	}
	digest, err := str("digest", true)
	if err != nil {
		return err
	}
	name, ok := strings.CutPrefix(image, legacyImagePrefix)
	if !ok || name == "" || strings.Contains(name, "..") || strings.Trim(name, "abcdefghijklmnopqrstuvwxyz0123456789._/-") != "" ||
		!legacyDigestRe.MatchString(digest) {
		return fmt.Errorf("image %s@%s", image, digest)
	}
	var installers map[string]json.RawMessage
	if json.Unmarshal(doc["installer"], &installers) != nil || installers == nil {
		return errors.New("installer is missing or not an object")
	}
	for arch, rawAsset := range installers {
		var asset map[string]json.RawMessage
		if json.Unmarshal(rawAsset, &asset) != nil || asset == nil {
			return fmt.Errorf("installer for %s is not an object", arch)
		}
		if err := legacyExactKeys(asset, []string{"url", "sha256"}); err != nil {
			return fmt.Errorf("installer for %s: %w", arch, err)
		}
		var url, sum *string
		if json.Unmarshal(asset["url"], &url) != nil || url == nil || json.Unmarshal(asset["sha256"], &sum) != nil || sum == nil {
			return fmt.Errorf("installer for %s: url and sha256 must be strings", arch)
		}
		if !legacySHA256Re.MatchString(*sum) || !strings.HasPrefix(*url, "https://") {
			return fmt.Errorf("installer for %s", arch)
		}
	}
	for _, arch := range legacyArchitectures {
		if _, ok := installers[arch]; !ok {
			return fmt.Errorf("no installer for %s", arch)
		}
	}
	if rawNotes, ok := doc["notes"]; ok {
		var notes map[string]*string
		if json.Unmarshal(rawNotes, &notes) != nil || notes == nil {
			return errors.New("notes is not an object")
		}
		for lang, text := range notes {
			if text == nil {
				return fmt.Errorf("notes for %s is not a string", lang)
			}
		}
	}
	return nil
}

// legacyExactKeys refuses a key that spells a known field in other letters: the Go panels
// match fields regardless of case, the installers do not, so the two would read two
// different values.
func legacyExactKeys(doc map[string]json.RawMessage, fields []string) error {
	for key := range doc {
		for _, f := range fields {
			if key != f && strings.EqualFold(key, f) {
				return fmt.Errorf("key %q spells %q in other letters", key, f)
			}
		}
	}
	return nil
}

// legacyStrictJSON walks the document: one value, no key twice in an object, no nesting
// deeper than serde_json reads.
func legacyStrictJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var walk func(depth int) error
	walk = func(depth int) error {
		if depth > legacyMaxDepth {
			return errors.New("nested deeper than the old clients read")
		}
		tok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("not JSON: %w", err)
		}
		switch tok {
		case json.Delim('{'):
			seen := map[string]bool{}
			for dec.More() {
				key, err := dec.Token()
				if err != nil {
					return fmt.Errorf("not JSON: %w", err)
				}
				k, _ := key.(string)
				if seen[k] {
					return fmt.Errorf("key %q twice in an object", k)
				}
				seen[k] = true
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
			_, err = dec.Token()
		case json.Delim('['):
			for dec.More() {
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
			_, err = dec.Token()
		}
		return err
	}
	if err := walk(0); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("data after the JSON value")
	}
	return nil
}

// legacyVersionParts is versionParts of 0.4.5 and 0.5.0.0 (and semver() of their
// installers, which accept exactly the same): three or four numbers that fit in 64 bits,
// and a pre-release of non-empty dot-separated parts.
func legacyVersionParts(v string) []string {
	p := legacyVersionRe.FindStringSubmatch(v)
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

// legacyCompare is compare() of 0.4.5 and 0.5.0.0: a pre-release before its release, an
// unparsable version before everything.
func legacyCompare(a, b string) int {
	pa, pb := legacyVersionParts(a), legacyVersionParts(b)
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
	x, y := strings.Split(pa[5], "."), strings.Split(pb[5], ".")
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
			if len(xs) != len(ys) {
				if len(xs) < len(ys) {
					return -1
				}
				return 1
			}
			if c := strings.Compare(xs, ys); c != 0 {
				return c
			}
			continue
		}
		return strings.Compare(x[i], y[i])
	}
	switch {
	case len(x) < len(y):
		return -1
	case len(x) > len(y):
		return 1
	}
	return 0
}
