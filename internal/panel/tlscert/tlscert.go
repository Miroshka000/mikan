package tlscert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"mikan/internal/fsutil"
	"mikan/internal/nodetls"
)

// Holder lets the certificate be swapped (ACME renewal, host change) without a restart.
type Holder struct {
	cert atomic.Pointer[tls.Certificate]
}

func (h *Holder) Set(c *tls.Certificate) { h.cert.Store(c) }

func (h *Holder) Get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	c := h.cert.Load()
	if c == nil {
		return nil, errors.New("no certificate")
	}
	return c, nil
}

// selfMu keeps the pair dir/self.{crt,key} whole: a certificate is made and written, or
// read, by one caller at a time, so concurrent callers (a subscription, a node's sync)
// never generate two pairs or read one half of each.
var selfMu sync.Mutex

// LoadOrCreateSelfSigned reuses dir/self.{crt,key} when it covers host and is not
// about to expire, otherwise writes a fresh ECDSA P-256 certificate.
func LoadOrCreateSelfSigned(dir, host string, now time.Time) (*tls.Certificate, error) {
	selfMu.Lock()
	defer selfMu.Unlock()
	certPath, keyPath := filepath.Join(dir, "self.crt"), filepath.Join(dir, "self.key")
	if c, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil && c.Leaf != nil && Covers(c.Leaf, host) && now.Add(30*24*time.Hour).Before(c.Leaf.NotAfter) {
		return &c, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: host},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(825 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else if host != "" {
		tmpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	if err := fsutil.WriteFileAtomic(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return nil, err
	}
	if err := fsutil.WriteFileAtomic(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		return nil, err
	}
	c, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("reload self-signed: %w", err)
	}
	return &c, nil
}

// PEM returns the certificate and key files as text plus the hex SHA-256 of the leaf,
// which clients pin when the certificate is self-signed.
func PEM(dir string) (certPEM, keyPEM, pin string, err error) {
	selfMu.Lock()
	defer selfMu.Unlock()
	c, err := os.ReadFile(filepath.Join(dir, "self.crt"))
	if err != nil {
		return "", "", "", err
	}
	k, err := os.ReadFile(filepath.Join(dir, "self.key"))
	if err != nil {
		return "", "", "", err
	}
	block, _ := pem.Decode(c)
	if block == nil {
		return "", "", "", errors.New("self.crt: no PEM block")
	}
	return string(c), string(k), nodetls.PinOfDER(block.Bytes), nil
}
