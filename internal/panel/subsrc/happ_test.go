package subsrc

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/chacha20poly1305"
)

// The local crypt link carries the address the way the app opens it: decrypted with the
// key pair's private half, it is the address, byte for byte.
func TestHappCryptRoundTrip(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range []string{"https://vpn.example.com:2096/sub/AbCdEfGhIjKlMnOpQrStUvWx", "https://пример.рф/sub/x?a=b+c&d=%2F#f"} {
		link, err := happCrypt(src, &key.PublicKey)
		if err != nil {
			t.Fatal(err)
		}
		if got := openHappLink(t, link, key); got != src {
			t.Fatalf("decrypted %q, want %q", got, src)
		}
	}
	for _, bad := range []string{"", "file:///etc/passwd", "https://u:p@example.com/s", "https:///s", "https://example.com/a\nb", "https://example.com/" + strings.Repeat("a", happSourceMax)} {
		if _, err := happCrypt(bad, &key.PublicKey); !errors.Is(err, ErrHappLink) {
			t.Errorf("%.40q: %v", bad, err)
		}
	}
	if _, err := happKey(); err != nil {
		t.Fatalf("the embedded key: %v", err)
	}
	if link, err := HappCryptLink("https://example.com/sub/x"); err != nil || !strings.HasPrefix(link, "happ://crypt5/") {
		t.Fatalf("HappCryptLink: %q %v", link, err)
	}
}

// openHappLink undoes happCrypt with the private key.
func openHappLink(t *testing.T, link string, key *rsa.PrivateKey) string {
	t.Helper()
	frame := []byte(strings.TrimPrefix(link, "happ://crypt5/"))
	for i := 0; i+3 < len(frame); i += 4 {
		frame[i], frame[i+2] = frame[i+2], frame[i]
		frame[i+1], frame[i+3] = frame[i+3], frame[i+1]
	}
	s := string(frame)
	if !strings.HasPrefix(s, happCrypt5Marker[:4]) || !strings.HasSuffix(s, happCrypt5Marker[4:]) {
		t.Fatalf("marker: %q", s[:8])
	}
	body := s[4 : len(s)-4]
	nonce, salt, rest := body[:12], body[14:22], body[22:]
	v := strings.IndexByte(rest, 'V')
	n, err := strconv.Atoi(rest[:v])
	if err != nil {
		t.Fatal(err)
	}
	cipherB64, rsaB64 := rest[v+1:v+1+n], rest[v+1+n:]
	rsaCipher, _ := base64.StdEncoding.DecodeString(rsaB64)
	wrappedB64, err := rsa.DecryptPKCS1v15(rand.Reader, key, rsaCipher)
	if err != nil {
		t.Fatal(err)
	}
	wrapped, _ := base64.StdEncoding.DecodeString(string(swapPairs(wrappedB64)))
	session := make([]byte, 32)
	for i := range session {
		session[i] = wrapped[i] ^ salt[i%8]
	}
	aead, _ := chacha20poly1305.New(session)
	cipher, _ := base64.StdEncoding.DecodeString(cipherB64)
	plain, err := aead.Open(nil, []byte(nonce), cipher, nil)
	if err != nil {
		t.Fatal(err)
	}
	src, _ := base64.StdEncoding.DecodeString(string(swapPairs(plain)))
	return string(src)
}

// Happ's service is asked once per address; when it fails or answers nonsense the panel
// makes the link itself, so the address stays hidden.
func TestHappLinks(t *testing.T) {
	var calls atomic.Int32
	answer := `{"encrypted_link":"happ://crypt5/fromservice"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "bad", 400)
			return
		}
		_, _ = w.Write([]byte(answer))
	}))
	defer srv.Close()
	l := NewHappLinks()
	l.API = srv.URL
	ctx := context.Background()
	for range 3 {
		if link, err := l.Link(ctx, HappCryptAPI, "https://vpn.example.com/sub/a"); err != nil || link != "happ://crypt5/fromservice" {
			t.Fatalf("service link: %q %v", link, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("the service was asked %d times", calls.Load())
	}
	answer = `{"encrypted_link":"https://evil.example/"}`
	if link, err := l.Link(ctx, HappCryptAPI, "https://vpn.example.com/sub/b"); err != nil || !strings.HasPrefix(link, "happ://crypt5/") || strings.Contains(link, "evil") {
		t.Fatalf("a bad answer falls back to the local link: %q %v", link, err)
	}
	srv.Close()
	if link, err := l.Link(ctx, HappCryptAPI, "https://vpn.example.com/sub/c"); err != nil || !strings.HasPrefix(link, "happ://crypt5/") {
		t.Fatalf("the service down: %q %v", link, err)
	}
	if _, err := l.Link(ctx, "", "https://vpn.example.com/sub/c"); !errors.Is(err, ErrHappLink) {
		t.Fatal("no mode, no link")
	}
}
