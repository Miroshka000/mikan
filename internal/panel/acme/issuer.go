package acme

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/registration"

	"mikan/internal/acmechallenge"
	"mikan/internal/fsutil"
	"mikan/internal/panel/settings"
)

// issuer orders certificates from the chosen CA for the panel and its nodes: one ACME
// account per CA, kept under root, and the challenge answered by whatever provider the
// order brings (the panel's own port 80, or a node's).
type issuer struct {
	root     string // <data>/tls/acme
	override string // MIKAN_ACME_DIRECTORY: one directory for every CA (tests, staging)
	set      *settings.Settings
	http     *http.Client // for ZeroSSL's EAB API
	keyMu    sync.Mutex   // one account key per CA, made once
}

func newIssuer(root string, set *settings.Settings) *issuer {
	return &issuer{root: root, override: os.Getenv("MIKAN_ACME_DIRECTORY"), set: set, http: &http.Client{Timeout: 20 * time.Second}}
}

func (i *issuer) directory(ca string) string {
	if i.override != "" {
		return i.override
	}
	return directories[ca]
}

// accountPath keeps Let's Encrypt's account where it always was.
func (i *issuer) accountPath(ca string) string {
	if ca == CALetsEncrypt {
		return filepath.Join(i.root, "account.key")
	}
	return filepath.Join(i.root, "accounts", ca+".key")
}

type user struct {
	email string
	reg   *registration.Resource
	key   crypto.PrivateKey
}

func (u *user) GetEmail() string                        { return u.email }
func (u *user) GetRegistration() *registration.Resource { return u.reg }
func (u *user) GetPrivateKey() crypto.PrivateKey        { return u.key }

// obtain orders a certificate for id from ca, the challenge answered by p. It returns the
// chain and the key in PEM.
func (i *issuer) obtain(ctx context.Context, id, ca string, p challenge.Provider) (certPEM, keyPEM []byte, err error) {
	key, err := i.accountKey(ca)
	if err != nil {
		return nil, nil, err
	}
	email, err := i.set.String(ctx, settings.KeyACMEEmail)
	if err != nil {
		return nil, nil, err
	}
	u := &user{email: email, key: key}
	req, noCN := order(id)
	cfg := lego.NewConfig(u)
	cfg.CADirURL = i.directory(ca)
	cfg.Certificate.KeyType = certcrypto.EC256
	cfg.Certificate.DisableCommonName = noCN
	client, err := lego.NewClient(cfg)
	if err != nil {
		return nil, nil, err
	}
	if err := client.Challenge.SetHTTP01Provider(p); err != nil {
		return nil, nil, err
	}
	if u.reg, err = client.Registration.ResolveAccountByKey(); err != nil {
		if u.reg, err = i.register(ctx, client, ca, email); err != nil {
			return nil, nil, &errRegister{err: err}
		}
	}
	res, err := client.Certificate.Obtain(req)
	if err != nil {
		return nil, nil, err
	}
	return res.Certificate, res.PrivateKey, nil
}

// register makes the account: plain at Let's Encrypt, bound to an external account at
// ZeroSSL (by the admin's e-mail) and Google (by the key the admin pasted).
func (i *issuer) register(ctx context.Context, client *lego.Client, ca, email string) (*registration.Resource, error) {
	switch ca {
	case CAZeroSSL:
		eab, err := zeroSSLEAB(ctx, i.http, email)
		if err != nil {
			return nil, err
		}
		return client.Registration.RegisterWithExternalAccountBinding(registration.RegisterEABOptions{TermsOfServiceAgreed: true, Kid: eab.KID, HmacEncoded: eab.HMAC})
	case CAGoogle:
		kid, err := i.set.String(ctx, settings.KeyACMEEABKID)
		if err != nil {
			return nil, err
		}
		hmac, err := i.set.String(ctx, settings.KeyACMEEABHMAC)
		if err != nil {
			return nil, err
		}
		if kid == "" || hmac == "" {
			return nil, ErrGoogleEAB
		}
		return client.Registration.RegisterWithExternalAccountBinding(registration.RegisterEABOptions{TermsOfServiceAgreed: true, Kid: kid, HmacEncoded: hmac})
	}
	return client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
}

func (i *issuer) accountKey(ca string) (crypto.PrivateKey, error) {
	i.keyMu.Lock()
	defer i.keyMu.Unlock()
	path := i.accountPath(ca)
	if raw, err := os.ReadFile(path); err == nil {
		block, _ := pem.Decode(raw)
		if block == nil {
			return nil, fmt.Errorf("%s: no PEM block", filepath.Base(path))
		}
		return x509.ParseECPrivateKey(block.Bytes)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		return nil, err
	}
	return k, fsutil.WriteFileAtomic(path, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600)
}

// order is what the CA is asked for. Let's Encrypt issues an IP certificate only with the
// shortlived profile and only with the IP in the SAN: an IP in the Common Name is refused
// as badCSR, so the CSR goes without one. A domain keeps its Common Name.
func order(id string) (req certificate.ObtainRequest, noCommonName bool) {
	req = certificate.ObtainRequest{Domains: []string{id}, Bundle: true}
	if net.ParseIP(id) != nil {
		req.Profile = "shortlived"
		return req, true
	}
	return req, false
}

// A certificate the panel ordered lives in a directory of its own: cert.pem (the chain),
// key.pem and ca (which CA issued it; none: Let's Encrypt, as before the choice).
const (
	pairCert = "cert.pem"
	pairKey  = "key.pem"
	pairCA   = "ca"
)

// pairMu keeps a pair whole: it is written and read by one caller at a time, so nobody
// reads the new chain with the old key.
var pairMu sync.RWMutex

func savePair(dir string, certPEM, keyPEM []byte, ca string) error {
	pairMu.Lock()
	defer pairMu.Unlock()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := fsutil.WriteFileAtomic(filepath.Join(dir, pairKey), keyPEM, 0o600); err != nil {
		return err
	}
	if err := fsutil.WriteFileAtomic(filepath.Join(dir, pairCert), certPEM, 0o600); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(filepath.Join(dir, pairCA), []byte(ca+"\n"), 0o600)
}

func loadPair(dir string) (*tls.Certificate, string, error) {
	pairMu.RLock()
	defer pairMu.RUnlock()
	c, err := tls.LoadX509KeyPair(filepath.Join(dir, pairCert), filepath.Join(dir, pairKey))
	if err != nil {
		return nil, "", err
	}
	if c.Leaf == nil {
		return nil, "", errors.New("no leaf certificate")
	}
	ca := CALetsEncrypt
	if raw, err := os.ReadFile(filepath.Join(dir, pairCA)); err == nil && ValidCA(strings.TrimSpace(string(raw))) {
		ca = strings.TrimSpace(string(raw))
	}
	return &c, ca, nil
}

// serverProvider answers the panel's own challenges on its port 80 (or MIKAN_ACME_LISTEN).
type serverProvider struct{ s *acmechallenge.Server }

func (p serverProvider) Present(_, token, keyAuth string) error { return p.s.Present(token, keyAuth) }
func (p serverProvider) CleanUp(_, token, _ string) error {
	p.s.CleanUp(token)
	return nil
}

// needsRenewal: once a third of the lifetime is left (~2 days for 6-day IP certificates,
// ~30 days for 90-day ones), leaving room for several retries.
func needsRenewal(leaf *x509.Certificate, now time.Time) bool {
	life := leaf.NotAfter.Sub(leaf.NotBefore)
	return leaf.NotAfter.Sub(now) < life/3
}
