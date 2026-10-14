package tlscert

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mikan/internal/fsutil"
	"mikan/internal/nodetls"
)

// The admin's own certificate, instead of Let's Encrypt or the self-signed one (GitHub
// issue #9): a chain and its key, checked on the way in, kept as custom.{crt,key}.

// MaxPEM bounds what is read: a chain of a few certificates and one key fit easily.
const MaxPEM = 64 << 10

var (
	ErrCertPEM     = errors.New("cert_pem_invalid")
	ErrKeyPEM      = errors.New("key_pem_invalid")
	ErrKeyMismatch = errors.New("cert_key_mismatch")
	ErrKeyWeak     = errors.New("cert_key_weak")
	ErrExpired     = errors.New("cert_expired")
	ErrNotYet      = errors.New("cert_not_yet_valid")
	ErrWrongHost   = errors.New("cert_wrong_host")
)

// Info is what the admin panel shows about a certificate.
type Info struct {
	Names    []string  `json:"names" doc:"Домены и IP, на которые он выписан"`
	Issuer   string    `json:"issuer"`
	NotAfter time.Time `json:"not_after"`
	// Trusted: the chain verifies against the system roots for the host, so clients
	// accept it as is and a renewal changes nothing for them.
	Trusted bool `json:"trusted" doc:"Публично доверенный для адреса: приложения принимают его без пина"`
}

// ParseCustom checks a certificate chain (leaf first) and its private key: both PEM,
// matching, a key of a usable strength, valid now.
func ParseCustom(certPEM, keyPEM []byte, now time.Time) (*tls.Certificate, error) {
	if len(certPEM) == 0 || len(certPEM) > MaxPEM {
		return nil, ErrCertPEM
	}
	if len(keyPEM) == 0 || len(keyPEM) > MaxPEM {
		return nil, ErrKeyPEM
	}
	var chain [][]byte
	for rest := certPEM; ; {
		var b *pem.Block
		if b, rest = pem.Decode(rest); b == nil {
			break
		}
		if b.Type != "CERTIFICATE" {
			continue
		}
		if _, err := x509.ParseCertificate(b.Bytes); err != nil {
			return nil, ErrCertPEM
		}
		chain = append(chain, b.Bytes)
	}
	if len(chain) == 0 {
		return nil, ErrCertPEM
	}
	leaf, _ := x509.ParseCertificate(chain[0])
	key, err := parseKey(keyPEM)
	if err != nil {
		return nil, err
	}
	pub, ok := key.Public().(interface{ Equal(crypto.PublicKey) bool })
	if !ok || !pub.Equal(leaf.PublicKey) {
		return nil, ErrKeyMismatch
	}
	if !strongKey(key) {
		return nil, ErrKeyWeak
	}
	switch {
	case now.Before(leaf.NotBefore):
		return nil, ErrNotYet
	case !now.Before(leaf.NotAfter):
		return nil, ErrExpired
	}
	return &tls.Certificate{Certificate: chain, PrivateKey: key, Leaf: leaf}, nil
}

func parseKey(keyPEM []byte) (crypto.Signer, error) {
	for rest := keyPEM; ; {
		var b *pem.Block
		if b, rest = pem.Decode(rest); b == nil {
			return nil, ErrKeyPEM
		}
		if !strings.HasSuffix(b.Type, "PRIVATE KEY") {
			continue
		}
		var k any
		var err error
		switch b.Type {
		case "RSA PRIVATE KEY":
			k, err = x509.ParsePKCS1PrivateKey(b.Bytes)
		case "EC PRIVATE KEY":
			k, err = x509.ParseECPrivateKey(b.Bytes)
		default:
			k, err = x509.ParsePKCS8PrivateKey(b.Bytes)
		}
		s, ok := k.(crypto.Signer)
		if err != nil || !ok {
			return nil, ErrKeyPEM
		}
		return s, nil
	}
}

func strongKey(k crypto.Signer) bool {
	switch k := k.(type) {
	case *rsa.PrivateKey:
		return k.N.BitLen() >= 2048
	case *ecdsa.PrivateKey:
		c := k.Curve
		return c == elliptic.P256() || c == elliptic.P384() || c == elliptic.P521()
	case ed25519.PrivateKey:
		return true
	}
	return false
}

// Covers: host (a name or an IP) is in the certificate, wildcards included.
func Covers(leaf *x509.Certificate, host string) bool {
	return host != "" && leaf.VerifyHostname(host) == nil
}

// Describe reports on a certificate; host is where clients reach it ("" when unknown).
func Describe(c *tls.Certificate, host string, now time.Time) Info {
	leaf := c.Leaf
	info := Info{Names: append([]string{}, leaf.DNSNames...), Issuer: leaf.Issuer.CommonName, NotAfter: leaf.NotAfter}
	for _, ip := range leaf.IPAddresses {
		info.Names = append(info.Names, ip.String())
	}
	if len(info.Names) == 0 && leaf.Subject.CommonName != "" {
		info.Names = []string{leaf.Subject.CommonName}
	}
	if info.Issuer == "" && len(leaf.Issuer.Organization) > 0 {
		info.Issuer = leaf.Issuer.Organization[0]
	}
	if host != "" {
		roots, err := x509.SystemCertPool()
		if err == nil {
			inter := x509.NewCertPool()
			for _, der := range c.Certificate[1:] {
				if ic, err := x509.ParseCertificate(der); err == nil {
					inter.AddCert(ic)
				}
			}
			_, err = leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter, DNSName: host, CurrentTime: now})
			info.Trusted = err == nil
		}
	}
	return info
}

const customCert, customKey = "custom.crt", "custom.key"

// SaveCustom writes the chain and key into dir, readable by the panel alone. Each file is
// written aside and renamed, so a reader never sees half of one.
func SaveCustom(dir string, c *tls.Certificate) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	var chain bytes.Buffer
	for _, der := range c.Certificate {
		_ = pem.Encode(&chain, &pem.Block{Type: "CERTIFICATE", Bytes: der})
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(c.PrivateKey)
	if err != nil {
		return err
	}
	// The key first: a new chain next to an old key would not load.
	if err := fsutil.WriteFileAtomic(filepath.Join(dir, customKey), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(filepath.Join(dir, customCert), chain.Bytes(), 0o600)
}

// LoadCustom reads the certificate SaveCustom wrote; fs.ErrNotExist when there is none.
// An expired one is returned with ErrExpired, for the caller to fall back and say why.
func LoadCustom(dir string, now time.Time) (*tls.Certificate, error) {
	certPEM, err := os.ReadFile(filepath.Join(dir, customCert))
	if err != nil {
		return nil, err
	}
	keyPEM, err := os.ReadFile(filepath.Join(dir, customKey))
	if err != nil {
		return nil, err
	}
	return ParseCustom(certPEM, keyPEM, now)
}

// CustomModTime changes whenever the files do; zero without them.
func CustomModTime(dir string) time.Time {
	var t time.Time
	for _, name := range []string{customCert, customKey} {
		if st, err := os.Stat(filepath.Join(dir, name)); err == nil && st.ModTime().After(t) {
			t = st.ModTime()
		}
	}
	return t
}

// HasCustom: the files are there, whatever is in them.
func HasCustom(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, customCert))
	return err == nil
}

// RemoveCustom deletes the files; none there is fine.
func RemoveCustom(dir string) error {
	for _, name := range []string{customCert, customKey} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// CustomPEM is the chain and key as text, for a node; the leaf's SHA-256 for a pin.
func CustomPEM(c *tls.Certificate) (certPEM, keyPEM string, err error) {
	var chain bytes.Buffer
	for _, der := range c.Certificate {
		_ = pem.Encode(&chain, &pem.Block{Type: "CERTIFICATE", Bytes: der})
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(c.PrivateKey)
	if err != nil {
		return "", "", err
	}
	return chain.String(), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})), nil
}

// Pin is the hex SHA-256 of the leaf, which clients pin when they cannot trust the chain.
func Pin(c *tls.Certificate) string {
	return nodetls.PinOfDER(c.Certificate[0])
}

// NodeStore keeps the admin's own certificate of each node, for its protocols on the
// node's TLS (Hysteria2, TUIC, AnyTLS, TrustTunnel…). What it learned of a certificate
// is kept until the files change: subscriptions ask on every request.
type NodeStore struct {
	dir string
	now func() time.Time

	mu    sync.Mutex
	cache map[int64]nodeCert

	generation atomic.Uint64 // moves with every certificate set or cleared
}

// Generation moves whenever a node's own certificate is set or cleared.
func (s *NodeStore) Generation() uint64 { return s.generation.Load() }

type nodeCert struct {
	mod     time.Time
	host    string
	cert    *tls.Certificate
	err     error
	trusted bool
}

func NewNodeStore(dir string, now func() time.Time) *NodeStore {
	return &NodeStore{dir: dir, now: now, cache: map[int64]nodeCert{}}
}

func (s *NodeStore) path(id int64) string { return filepath.Join(s.dir, strconv.FormatInt(id, 10)) }

// Set checks and keeps a node's certificate.
func (s *NodeStore) Set(id int64, certPEM, keyPEM []byte) (*tls.Certificate, error) {
	c, err := ParseCustom(certPEM, keyPEM, s.now())
	if err != nil {
		return nil, err
	}
	defer s.generation.Add(1)
	return c, SaveCustom(s.path(id), c)
}

func (s *NodeStore) Clear(id int64) error {
	defer s.generation.Add(1)
	return RemoveCustom(s.path(id))
}

// Get is the node's own certificate and whether clients reach host trusting it; nil
// without one. err says why one that is there is not used (expired, broken).
func (s *NodeStore) Get(id int64, host string) (cert *tls.Certificate, trusted bool, err error) {
	dir := s.path(id)
	mod := CustomModTime(dir)
	if mod.IsZero() {
		return nil, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.cache[id]; ok && c.mod.Equal(mod) && c.host == host && (c.cert == nil || s.now().Before(c.cert.Leaf.NotAfter)) {
		return c.cert, c.trusted, c.err
	}
	c := nodeCert{mod: mod, host: host}
	if c.cert, c.err = LoadCustom(dir, s.now()); c.err == nil {
		c.trusted = Describe(c.cert, host, s.now()).Trusted
	} else {
		c.cert = nil
	}
	s.cache[id] = c
	return c.cert, c.trusted, c.err
}
