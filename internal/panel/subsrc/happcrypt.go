package subsrc

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/chacha20poly1305"

	"mikan/internal/httpjson"
)

// A Happ crypt link (happ://crypt5/…) carries the subscription address so that the app
// opens it and the user never sees it. Happ's own service makes them; the format is not
// published, and the local way below follows 3x-ui (GPL-3.0, MHSanaei/3x-ui#6494), which
// took the key from Omegaplexx/hpwnr. If Happ changes the format, the service keeps working.

// ErrHappLink: no crypt link now; the caller shows no Happ button rather than the plain
// address it was told to hide.
var ErrHappLink = errors.New("happ_link")

// How the crypt link is made (Config.HappCrypt).
const (
	HappCryptAPI   = "api"   // by Happ's own service, the panel itself when it is down
	HappCryptLocal = "local" // by the panel itself
)

// HappCryptAPIURL is Happ's own service (happ.su/main/dev-docs/crypto-link).
const HappCryptAPIURL = "https://crypto.happ.su/api-v2.php"

const happPublicKeyPEM = `-----BEGIN PUBLIC KEY-----
MIICIjANBgkqhkiG9w0BAQEFAAOCAg8AMIICCgKCAgEA9+umWSxp8coKnMONnI4u
NvtPErJZt8VNgNb2XS+RrCMc9AFWZQH01ILr3Py/mviuqFgNLMEcPs3k6+ZPh6Sa
OCXHmjQicGPJAw6Co6GQwO/b4vspHgOM4HSvX5r6SY1EKIHUSLIyRV28DfwJKdFv
x2EKqypewlrAo4AV76uI/9U+1t40yHcVCj/OtFxsq+mMM6qySieTsA1q6C5raBrJ
u3l/RWMxFYvDInYDs1IaTFGDFwSdFDqhNU19gPGloT/GApy+U32R6AGSxJymS2nh
e6pm/M9bvsH0o0Oc1kyXsBpVN04n/a9gVVUoqODzrUyXDx7/jAzNJD43PWtblcz0
ZNBKN50wvpSD5UuAQydwMT7xWJIpPaZqTUj/sg8hIm57XGlUxRCge17nB0Ff7sKO
JAgaXVdbfqDdzx+PhSaZY9xfcAh/sHfE6hKaCQ9kIn5cjbx9bcYqZWnpuSOzSFg+
CgMSqvG6rV6d+96dNMHuE0tRIUJ83xrLcm9hZJmJ6WDm6hteZbnb1k3eQF9c+XCF
wSEvsWiXyduQmkVNJaCRXwy8tSaZp9JftALhRHMvd7Eq6ctAkvn7w0upynsAtLeL
N8xZ5q1gcRgboydr588D3m8KF7mVuX/XRp2AG7hzyYdkQov9bfEfXIaBVlwHMKhy
uPTxeM4Les6fvaHMSWJ+8EUCAwEAAQ==
-----END PUBLIC KEY-----`

const (
	happCrypt5Marker = "vdfzfoff"
	// The marker picks the app's private key: a different public key would make links the
	// app cannot open, so the embedded one is pinned.
	happKeyFingerprint = "22319c7b13647897bf5fd4f827ba92bf3946d738007a0054ccd931c31f221768"
	happSourceMax      = 8192
)

var happKey = sync.OnceValues(func() (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(happPublicKeyPEM))
	if block == nil {
		return nil, ErrHappLink
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*rsa.PublicKey)
	if !ok || key.N.BitLen() != 4096 || key.E != 65537 {
		return nil, ErrHappLink
	}
	sum := sha256.Sum256(block.Bytes)
	if hex.EncodeToString(sum[:]) != happKeyFingerprint {
		return nil, ErrHappLink
	}
	return key, nil
})

// happSource checks the address a crypt link carries: an absolute http(s) URL.
func happSource(source string) error {
	if source == "" || len(source) > happSourceMax || strings.ContainsFunc(source, func(r rune) bool { return r < ' ' || r == 0x7f }) {
		return ErrHappLink
	}
	u, err := url.Parse(source)
	if err != nil || u.Opaque != "" || u.User != nil || u.Hostname() == "" || u.Scheme != "http" && u.Scheme != "https" {
		return ErrHappLink
	}
	return nil
}

// HappCryptLink makes a crypt5 link without Happ's service: a ChaCha20-Poly1305 session
// key wrapped by the app's RSA key, in the framing the app reads.
func HappCryptLink(source string) (string, error) {
	key, err := happKey()
	if err != nil {
		return "", err
	}
	return happCrypt(source, key)
}

func happCrypt(source string, key *rsa.PublicKey) (string, error) {
	if err := happSource(source); err != nil {
		return "", err
	}
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	const alnum = letters + "0123456789"
	session := make([]byte, 32)
	if _, err := rand.Read(session); err != nil {
		return "", err
	}
	nonce, tag, salt := randomChars(12, alnum), randomChars(2, letters), randomChars(8, alnum)
	wrapped := make([]byte, 32)
	for i := range wrapped {
		wrapped[i] = session[i] ^ salt[i%8]
	}
	//nolint:staticcheck // the app expects PKCS#1 v1.5: OAEP is another wire format
	rsaCipher, err := rsa.EncryptPKCS1v15(rand.Reader, key, swapPairs([]byte(base64.StdEncoding.EncodeToString(wrapped))))
	if err != nil {
		return "", err
	}
	aead, err := chacha20poly1305.New(session)
	if err != nil {
		return "", err
	}
	plain := swapPairs([]byte(base64.StdEncoding.EncodeToString([]byte(source))))
	cipher := base64.StdEncoding.EncodeToString(aead.Seal(nil, nonce, plain, nil))
	body := string(nonce) + string(tag) + string(salt) + strconv.Itoa(len(cipher)) + "V" + cipher + base64.StdEncoding.EncodeToString(rsaCipher)
	frame := []byte(happCrypt5Marker[:4] + body + happCrypt5Marker[4:])
	for i := 0; i+3 < len(frame); i += 4 {
		frame[i], frame[i+2] = frame[i+2], frame[i]
		frame[i+1], frame[i+3] = frame[i+3], frame[i+1]
	}
	return "happ://crypt5/" + string(frame), nil
}

func randomChars(n int, alphabet string) []byte {
	out := make([]byte, n)
	limit := big.NewInt(int64(len(alphabet)))
	for i := range out {
		x, err := rand.Int(rand.Reader, limit)
		if err != nil {
			panic(err) // crypto/rand does not fail on supported systems
		}
		out[i] = alphabet[x.Int64()]
	}
	return out
}

func swapPairs(b []byte) []byte {
	for i := 0; i+1 < len(b); i += 2 {
		b[i], b[i+1] = b[i+1], b[i]
	}
	return b
}

// HappLinks makes crypt links and keeps them: a link stays valid as long as its address,
// and Happ's service is asked once per subscription, not on every page view.
type HappLinks struct {
	API    string // HappCryptAPIURL; a test server in tests
	Client *http.Client

	mu    sync.Mutex
	cache map[string]string // mode + address → link
}

// happLinksMax bounds the cache; past it, it starts over (links are cheap to make again).
const happLinksMax = 50000

func NewHappLinks() *HappLinks {
	return &HappLinks{API: HappCryptAPIURL, Client: &http.Client{Timeout: 8 * time.Second}}
}

// Link is the crypt link of source the way mode says. With the service down the panel
// makes it itself: the address stays hidden either way.
func (l *HappLinks) Link(ctx context.Context, mode, source string) (string, error) {
	if mode != HappCryptAPI && mode != HappCryptLocal {
		return "", ErrHappLink
	}
	if err := happSource(source); err != nil {
		return "", err
	}
	k := mode + " " + source
	l.mu.Lock()
	link, ok := l.cache[k]
	l.mu.Unlock()
	if ok {
		return link, nil
	}
	var err error
	if mode == HappCryptAPI {
		link, err = l.fromAPI(ctx, source)
	}
	if mode == HappCryptLocal || err != nil {
		link, err = HappCryptLink(source)
	}
	if err != nil {
		return "", err
	}
	l.mu.Lock()
	if l.cache == nil || len(l.cache) >= happLinksMax {
		l.cache = map[string]string{}
	}
	l.cache[k] = link
	l.mu.Unlock()
	return link, nil
}

func (l *HappLinks) fromAPI(ctx context.Context, source string) (string, error) {
	body, _ := json.Marshal(map[string]string{"url": source})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.API, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	var out struct {
		Link string `json:"encrypted_link"`
	}
	if err := httpjson.Do(l.Client, req, 64<<10, &out); err != nil {
		if code := httpjson.Code(err); code != 0 {
			return "", fmt.Errorf("happ crypt service: %d", code)
		}
		return "", err
	}
	if !strings.HasPrefix(out.Link, "happ://crypt") || strings.ContainsAny(out.Link, " \r\n\t\"'<>") {
		return "", errors.New("happ crypt service: not a crypt link")
	}
	return out.Link, nil
}
