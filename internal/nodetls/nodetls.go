// Package nodetls secures the Node API between the panel and a remote node. Both sides
// use self-signed Ed25519 certificates pinned by SHA-256: the panel keeps one client
// certificate, and every node gets its own server certificate when it joins. The join
// key carries the node's certificate and the panel's pin, so issuing a new key for a
// node revokes the old one, and a leaked key cannot impersonate the panel.
package nodetls

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Pair is a certificate with its private key, both PEM.
type Pair struct {
	CertPEM string
	KeyPEM  string
}

// Generate creates a self-signed certificate valid for 20 years.
func Generate(name string, usage x509.ExtKeyUsage, now time.Time) (Pair, error) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Pair{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return Pair{}, err
	}
	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: name},
		DNSNames:     []string{name},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.AddDate(20, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, pub, key)
	if err != nil {
		return Pair{}, err
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return Pair{}, err
	}
	return Pair{
		CertPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		KeyPEM:  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})),
	}, nil
}

// Fingerprint is the hex SHA-256 of the certificate's DER.
func Fingerprint(certPEM string) (string, error) {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil || block.Type != "CERTIFICATE" {
		return "", errors.New("no certificate")
	}
	sum := sha256.Sum256(block.Bytes)
	return hex.EncodeToString(sum[:]), nil
}

// LoadOrCreate keeps the panel's client certificate in dir.
func LoadOrCreate(dir string, now time.Time) (Pair, error) {
	certPath, keyPath := filepath.Join(dir, "client.pem"), filepath.Join(dir, "client.key")
	c, errC := os.ReadFile(certPath)
	k, errK := os.ReadFile(keyPath)
	if errC == nil && errK == nil {
		return Pair{CertPEM: string(c), KeyPEM: string(k)}, nil
	}
	p, err := Generate("mikan-panel", x509.ExtKeyUsageClientAuth, now)
	if err != nil {
		return Pair{}, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Pair{}, err
	}
	if err := os.WriteFile(keyPath, []byte(p.KeyPEM), 0o600); err != nil {
		return Pair{}, err
	}
	return p, os.WriteFile(certPath, []byte(p.CertPEM), 0o600)
}

// Key is what a remote node needs to serve the Node API to its panel.
type Key struct {
	Port      int    `json:"port"`
	PanelPin  string `json:"panel_sha256"`
	CertPEM   string `json:"cert"`
	KeyPEM    string `json:"key"`
	NodeLabel string `json:"name,omitempty"`
	// PanelURL is where the node says hello once it runs (https://host:port, no path): the
	// panel then dials it back at once and the installer prints how that went. Keys of
	// panels before it have none, and a node of such a key just waits for its panel.
	PanelURL string `json:"panel,omitempty"`
	// Host is where clients reach the node, its domain or IP: the name its certificate is
	// ordered for. The installer names it in the command that lets Let's Encrypt through a
	// web server on port 80. Keys of older panels have none.
	Host string `json:"host,omitempty"`
}

const keyPrefix = "mikan1."

// Encode renders the join key as one shell-safe word.
func (k Key) Encode() (string, error) {
	raw, err := json.Marshal(k)
	if err != nil {
		return "", err
	}
	return keyPrefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

// DecodeKey parses a join key and checks that it is complete.
func DecodeKey(s string) (Key, error) {
	var k Key
	body, ok := strings.CutPrefix(strings.TrimSpace(s), keyPrefix)
	if !ok {
		return k, errors.New("not a mikan node key")
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return k, fmt.Errorf("node key: %w", err)
	}
	if err := json.Unmarshal(raw, &k); err != nil {
		return k, fmt.Errorf("node key: %w", err)
	}
	if k.Port < 1 || k.Port > 65535 || len(k.PanelPin) != 64 {
		return k, errors.New("node key: bad port or panel pin")
	}
	if _, err := tls.X509KeyPair([]byte(k.CertPEM), []byte(k.KeyPEM)); err != nil {
		return k, fmt.Errorf("node key: %w", err)
	}
	// The hello is a convenience: a panel address that is not one is dropped, not fatal.
	if !ValidPanelURL(k.PanelURL) {
		k.PanelURL = ""
	}
	return k, nil
}

// ValidPanelURL says whether s is a panel's base address: https, a host, no user, path,
// query or fragment.
func ValidPanelURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil &&
		(u.Path == "" || u.Path == "/") && u.RawQuery == "" && u.Fragment == "" && u.Opaque == ""
}

// Sign signs msg with a PEM private key of Generate.
func Sign(keyPEM string, msg []byte) ([]byte, error) {
	block, _ := pem.Decode([]byte(keyPEM))
	if block == nil {
		return nil, errors.New("nodetls: no private key")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	priv, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("nodetls: not an Ed25519 key")
	}
	return ed25519.Sign(priv, msg), nil
}

// Verify checks sig over msg against the key of a PEM certificate and returns the
// certificate's pin (as Fingerprint).
func Verify(certPEM string, msg, sig []byte) (string, error) {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil || block.Type != "CERTIFICATE" {
		return "", errors.New("nodetls: no certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", err
	}
	pub, ok := cert.PublicKey.(ed25519.PublicKey)
	if !ok {
		return "", errors.New("nodetls: not an Ed25519 certificate")
	}
	if !ed25519.Verify(pub, msg, sig) {
		return "", errors.New("nodetls: bad signature")
	}
	sum := sha256.Sum256(block.Bytes)
	return hex.EncodeToString(sum[:]), nil
}

// SamePin compares two pins in constant time.
func SamePin(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// ServerConfig is the node side: TLS 1.3, and only the panel's pinned certificate may connect.
func (k Key) ServerConfig() (*tls.Config, error) {
	cert, err := tls.X509KeyPair([]byte(k.CertPEM), []byte(k.KeyPEM))
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:            tls.VersionTLS13,
		Certificates:          []tls.Certificate{cert},
		ClientAuth:            tls.RequireAnyClientCert,
		VerifyPeerCertificate: pinned(k.PanelPin),
	}, nil
}

// ClientConfig is the panel side for one node: it presents the panel's certificate and
// accepts only the node certificate with the pinned fingerprint.
func ClientConfig(panel Pair, nodePin string) (*tls.Config, error) {
	cert, err := tls.X509KeyPair([]byte(panel.CertPEM), []byte(panel.KeyPEM))
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		// Self-signed on both sides: the pin replaces chain verification.
		InsecureSkipVerify:    true,
		VerifyPeerCertificate: pinned(nodePin),
	}, nil
}

func pinned(pin string) func([][]byte, [][]*x509.Certificate) error {
	want, _ := hex.DecodeString(pin)
	return func(raw [][]byte, _ [][]*x509.Certificate) error {
		if len(raw) == 0 || len(want) != sha256.Size {
			return errors.New("nodetls: no certificate to pin")
		}
		sum := sha256.Sum256(raw[0])
		if subtle.ConstantTimeCompare(sum[:], want) != 1 {
			return errors.New("nodetls: certificate does not match the pin")
		}
		return nil
	}
}
