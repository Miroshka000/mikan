package release

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"strings"
)

// Verify checks a signature of the release key over exact bytes: a manifest, the release
// index, the marketplace's catalog. ErrSignature when it does not hold.
func Verify(data []byte, sig string, pub ed25519.PublicKey) error {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sig))
	if err != nil || len(pub) != ed25519.PublicKeySize || !ed25519.Verify(pub, data, raw) {
		return ErrSignature
	}
	return nil
}

// ValidDigest says whether s is an image digest as a manifest or the catalog names one.
func ValidDigest(s string) bool { return digestRe.MatchString(s) }

// Getter reads url whole, at most limit bytes.
type Getter func(ctx context.Context, url string, limit int64) ([]byte, error)

// FetchSigned reads a signed file (up to limit) and its signature (url+".sig") and hands
// both to parse. They are two requests: a release published between them leaves the new
// file with the old signature, so a pair that does not match is asked for once more.
func FetchSigned(ctx context.Context, get Getter, url string, limit int64, parse func(data []byte, sig string) error) error {
	once := func() error {
		data, err := get(ctx, url, limit)
		if err != nil {
			return err
		}
		sig, err := get(ctx, url+".sig", 4096)
		if err != nil {
			return err
		}
		return parse(data, string(sig))
	}
	err := once()
	if errors.Is(err, ErrSignature) {
		err = once()
	}
	return err
}
