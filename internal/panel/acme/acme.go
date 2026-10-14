// Package acme obtains and renews the panel's HTTPS certificate from Let's Encrypt:
// a normal certificate for a domain, or a short-lived one (profile "shortlived") for a
// bare IP address. Subscription URLs are fetched by client apps that reject
// self-signed certificates, so an IP-only install still needs a public certificate.
package acme

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge/http01"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/registration"

	"mikan/internal/fsutil"
	"mikan/internal/hostname"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/tlscert"
)

const letsEncrypt = "https://acme-v02.api.letsencrypt.org/directory"

// defaultChallenge is where the HTTP-01 challenge is answered unless MIKAN_ACME_LISTEN
// says otherwise: port 80 on every address, where Let's Encrypt comes.
const defaultChallenge = ":80"

type Status struct {
	Kind       string    `json:"kind" enum:"self-signed,letsencrypt,custom"`
	Identifier string    `json:"identifier"`
	NotAfter   time.Time `json:"not_after"`
	Error      string    `json:"error,omitempty"`
	CheckedAt  time.Time `json:"checked_at"`
	// The admin's own certificate (kind custom).
	Issuer  string   `json:"issuer,omitempty"`
	Names   []string `json:"names,omitempty"`
	Trusted bool     `json:"trusted,omitempty" doc:"Свой сертификат публично доверенный для адреса панели"`
}

type Manager struct {
	dir       string
	directory string
	holder    *tlscert.Holder
	fallback  *tls.Certificate
	set       *settings.Settings
	log       *slog.Logger
	now       func() time.Time
	mu        sync.Mutex // the state below and the serving certificate; never held across an order
	orderMu   sync.Mutex // one order at a time: it takes minutes when port 80 hangs
	status    atomic.Pointer[Status]
	wake      chan struct{}
	challenge string // listen address for http-01: MIKAN_ACME_LISTEN, ":80" by default
	// customDir holds the admin's own certificate (tlscert.SaveCustom); it wins over
	// Let's Encrypt while valid. customMod is when its files last changed, as ensure saw.
	customDir string
	customMod time.Time
	// onChange hears of every change of Public: the local node gets that certificate with
	// its state, which is sent again only on a change. announced is the last one told.
	annMu     sync.Mutex
	onChange  func()
	announced string
}

func New(dataDir string, holder *tlscert.Holder, fallback *tls.Certificate, set *settings.Settings, log *slog.Logger, now func() time.Time) *Manager {
	dir := os.Getenv("MIKAN_ACME_DIRECTORY")
	if dir == "" {
		dir = letsEncrypt
	}
	challenge, err := ChallengeListen(os.Getenv("MIKAN_ACME_LISTEN"))
	if err != nil {
		log.Warn("MIKAN_ACME_LISTEN is not host:port: the HTTP-01 challenge is answered on "+defaultChallenge, "err", err)
		challenge = defaultChallenge
	}
	m := &Manager{dir: filepath.Join(dataDir, "tls", "acme"), directory: dir, holder: holder, fallback: fallback,
		set: set, log: log, now: now, wake: make(chan struct{}, 1), challenge: challenge, customDir: filepath.Join(dataDir, "tls", "custom")}
	m.status.Store(&Status{Kind: "self-signed", CheckedAt: now()})
	return m
}

// ChallengeListen reads MIKAN_ACME_LISTEN, the address the HTTP-01 challenge is answered
// on: ":80" when empty. When nginx or caddy holds port 80 the installer sets a loopback
// port, e.g. 127.0.0.1:18080, and the proxy forwards /.well-known/acme-challenge/ there.
// The host is an IP address, localhost or empty (every address); the port is 1..65535.
func ChallengeListen(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return defaultChallenge, nil
	}
	host, port, err := net.SplitHostPort(v)
	if err != nil {
		return "", err
	}
	if host != "" && host != "localhost" && net.ParseIP(host) == nil {
		return "", fmt.Errorf("host %q is not an IP address", host)
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return "", fmt.Errorf("port %q is not 1..65535", port)
	}
	return net.JoinHostPort(host, strconv.Itoa(p)), nil
}

func (m *Manager) Status() Status { return *m.status.Load() }

// OnChange sets what is called after Public changes: a certificate issued, renewed,
// uploaded or dropped. It is called outside the manager's locks and must not block.
func (m *Manager) OnChange(f func()) {
	m.annMu.Lock()
	defer m.annMu.Unlock()
	m.onChange = f
}

// Load serves the best certificate already on disk, with no network: called before the
// nodes are first synced, so the local node starts with the certificate the links expect.
func (m *Manager) Load(ctx context.Context) { m.settle(ctx) }

// announce calls onChange when Public is not the certificate last announced.
func (m *Manager) announce() {
	key := ""
	if c := m.Public(); c != nil {
		sum := sha256.Sum256(c.Leaf.Raw)
		key = hex.EncodeToString(sum[:])
	}
	m.annMu.Lock()
	if key == m.announced {
		m.annMu.Unlock()
		return
	}
	m.announced = key
	f := m.onChange
	m.annMu.Unlock()
	if f != nil {
		f()
	}
}

// Trusted says whether the panel serves a certificate browsers trust for its address: one
// from Let's Encrypt, or the admin's own when it is publicly trusted and covers the address.
func (m *Manager) Trusted() bool {
	st := m.status.Load()
	switch st.Kind {
	case "letsencrypt":
		return true
	case "custom":
		return st.Trusted && st.Error == ""
	}
	return false
}

// Public is the certificate the panel serves when clients trust it without a pin: the one
// from Let's Encrypt, or the admin's own that is publicly trusted. nil otherwise.
func (m *Manager) Public() *tls.Certificate {
	if !m.Trusted() {
		return nil
	}
	c, err := m.holder.Get(nil)
	if err != nil || c.Leaf == nil {
		return nil
	}
	return c
}

// Renew asks the background loop to try again now (e.g. after the admin freed port 80).
func (m *Manager) Renew() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// How often the certificate is looked after, and how soon after an order that failed. A
// six-day certificate for an IP leaves two days to renew in: a port 80 that was busy once
// must not cost the next six hours.
var (
	checkEvery = 6 * time.Hour
	retryAfter = 30 * time.Minute
)

func (m *Manager) Run(ctx context.Context) {
	next := time.NewTimer(checkEvery)
	defer next.Stop()
	// certbot, acme.sh or Caddy renew a custom certificate in place: a changed file is
	// served within half a minute, no restart.
	watch := time.NewTicker(30 * time.Second)
	defer watch.Stop()
	later := func(failed bool) {
		d := checkEvery
		if failed {
			d = retryAfter
		}
		if !next.Stop() {
			select {
			case <-next.C:
			default:
			}
		}
		next.Reset(d)
	}
	later(m.ensure(ctx))
	for {
		select {
		case <-ctx.Done():
			return
		case <-next.C:
		case <-m.wake:
		case <-watch.C:
			if !m.customChanged() {
				continue
			}
		}
		later(m.ensure(ctx))
	}
}

func (m *Manager) customChanged() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return !tlscert.CustomModTime(m.customDir).Equal(m.customMod)
}

// SetCustom installs the admin's own certificate: checked, kept, served at once. It has
// to cover the panel's address, or every subscription link would fail.
func (m *Manager) SetCustom(ctx context.Context, certPEM, keyPEM []byte) error {
	cert, err := tlscert.ParseCustom(certPEM, keyPEM, m.now())
	if err != nil {
		return err
	}
	id, err := m.identifier(ctx)
	if err != nil {
		return err
	}
	if id != "" && !tlscert.Covers(cert.Leaf, id) {
		return tlscert.ErrWrongHost
	}
	if err := tlscert.SaveCustom(m.customDir, cert); err != nil {
		return err
	}
	// Served at once, whatever order is running: the lock is not held across one.
	m.settle(ctx)
	return nil
}

// ClearCustom goes back to Let's Encrypt or the self-signed certificate. The custom one
// is served until the loop has the other: there is no gap.
func (m *Manager) ClearCustom() error {
	if err := tlscert.RemoveCustom(m.customDir); err != nil {
		return err
	}
	m.Renew()
	return nil
}

func (m *Manager) identifier(ctx context.Context) (string, error) {
	d, err := m.set.String(ctx, settings.KeyDomain)
	if err != nil || d != "" {
		return d, err
	}
	return m.set.String(ctx, settings.KeyPublicHost)
}

// subDomain is the subscription links' own domain, "" when they ride the panel's address.
func (m *Manager) subDomain(ctx context.Context) (string, error) {
	d, err := m.set.String(ctx, settings.KeySubDomain)
	return strings.TrimSpace(d), err
}

// ensure makes the panel serve the right certificate and, when it needs a new one from
// Let's Encrypt, orders it. It reports whether that order failed. The order runs outside
// m.mu: uploading a certificate of one's own must not wait for it.
func (m *Manager) ensure(ctx context.Context) (orderFailed bool) {
	id, need := m.settle(ctx)
	if !need {
		return false
	}
	m.orderMu.Lock()
	defer m.orderMu.Unlock()
	// Checked again: a certificate may have come meanwhile, an order that waited for ours.
	if id, need = m.settle(ctx); !need {
		return false
	}
	sub, err := m.subDomain(ctx)
	if err != nil {
		m.log.Warn("acme: subscription domain not read", "err", err)
	}
	cert, err := m.obtain(ctx, id, sub)
	if err != nil {
		m.log.Warn("acme: certificate not obtained", "identifier", id, "err", err)
		m.mu.Lock()
		st := *m.status.Load()
		if st.Kind == "custom" {
			// The admin's own certificate came while the order ran (SetCustom settled it into
			// the status): nothing is wrong.
			m.mu.Unlock()
			return false
		}
		if st.Error == "" { // a custom certificate that is broken is the thing to tell the admin of
			st.Error = humanError(err)
		}
		st.CheckedAt = m.now()
		if st.Kind == "self-signed" {
			m.useFallback()
		}
		m.status.Store(&st)
		m.mu.Unlock()
		m.announce()
		return true
	}
	m.log.Info("acme: certificate installed", "identifier", id, "not_after", cert.Leaf.NotAfter)
	m.settle(ctx) // now on disk: served, unless the admin's own certificate has come in the meantime
	return false
}

// settle serves the best certificate there already is and says whether a new one has to
// be ordered for id. It does no network.
func (m *Manager) settle(ctx context.Context) (id string, order bool) {
	defer m.announce() // after the lock and the status: Public reads both
	m.mu.Lock()
	defer m.mu.Unlock()
	id, err := m.identifier(ctx)
	sub, serr := m.subDomain(ctx)
	st := &Status{Kind: "self-signed", Identifier: id, CheckedAt: m.now()}
	defer func() { m.status.Store(st) }()
	if err != nil || serr != nil {
		st.Error = errors.Join(err, serr).Error()
		return id, false
	}
	// The admin's own certificate wins while it is valid; an expired or broken one falls
	// back to the rest, and the status says why.
	m.customMod = tlscert.CustomModTime(m.customDir)
	if tlscert.HasCustom(m.customDir) {
		cert, err := tlscert.LoadCustom(m.customDir, m.now())
		if err == nil {
			info := tlscert.Describe(cert, id, m.now())
			m.holder.Set(cert)
			st.Kind, st.NotAfter, st.Issuer, st.Names, st.Trusted = "custom", cert.Leaf.NotAfter, info.Issuer, info.Names, info.Trusted
			if id != "" && !tlscert.Covers(cert.Leaf, id) {
				st.Error = "custom_wrong_host"
			}
			return id, false
		}
		why := "custom_invalid"
		if errors.Is(err, tlscert.ErrExpired) {
			why = "custom_expired"
		}
		m.log.Warn("tls: the custom certificate is not used", "err", err)
		// What the admin set up and lost says more than why the fallback is what it is.
		defer func() { st.Error = why }()
	}
	if isPrivate(id) {
		m.useFallback()
		st.Error = "no_public_host"
		return id, false
	}
	// The subscription domain must be covered too, or its links fail the handshake. An
	// address in the identifier gets in the way: Let's Encrypt issues IP certificates
	// only in the shortlived profile, with the IP alone — no DNS name beside it. Whoever
	// fronts the subscription domain then keeps its TLS; the links are built all the same.
	subMust := sub != "" && net.ParseIP(id) == nil
	if sub != "" && net.ParseIP(id) != nil && !tlscert.HasCustom(m.customDir) {
		st.Error = "sub_domain_no_tls"
	}
	var leaf *x509.Certificate
	if cert, err := m.load(); err == nil && tlscert.Covers(cert.Leaf, id) {
		m.holder.Set(cert) // kept serving while the missing name, if any, is ordered
		st.Kind, st.NotAfter = "letsencrypt", cert.Leaf.NotAfter
		leaf = cert.Leaf
	}
	if leaf != nil && !m.needsRenewal(leaf) && (!subMust || tlscert.Covers(leaf, sub)) && (st.Error == "" || st.Error == "sub_domain_no_tls") {
		return id, false
	}
	if st.Kind == "self-signed" {
		m.useFallback()
	}
	if st.Error == "sub_domain_no_tls" {
		return id, false // nothing to order: the subscription host's TLS is not ours to take
	}
	return id, true
}

func (m *Manager) useFallback() {
	if m.fallback != nil {
		m.holder.Set(m.fallback)
	}
}

// needsRenewal renews once a third of the lifetime is left: ~2 days for 6-day IP
// certificates, ~30 days for 90-day ones, leaving room for several retries.
func (m *Manager) needsRenewal(leaf *x509.Certificate) bool {
	life := leaf.NotAfter.Sub(leaf.NotBefore)
	return leaf.NotAfter.Sub(m.now()) < life/3
}

type user struct {
	email string
	reg   *registration.Resource
	key   crypto.PrivateKey
}

func (u *user) GetEmail() string                        { return u.email }
func (u *user) GetRegistration() *registration.Resource { return u.reg }
func (u *user) GetPrivateKey() crypto.PrivateKey        { return u.key }

func (m *Manager) obtain(ctx context.Context, id, sub string) (*tls.Certificate, error) {
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return nil, err
	}
	key, err := m.accountKey()
	if err != nil {
		return nil, err
	}
	email, _ := m.set.String(ctx, settings.KeyACMEEmail)
	u := &user{email: email, key: key}
	req, noCN := order(id, sub)
	cfg := lego.NewConfig(u)
	cfg.CADirURL = m.directory
	cfg.Certificate.KeyType = certcrypto.EC256
	cfg.Certificate.DisableCommonName = noCN
	client, err := lego.NewClient(cfg)
	if err != nil {
		return nil, err
	}
	// The challenge's port (80, or MIKAN_ACME_LISTEN's) is taken only for the few seconds
	// of the challenge.
	host, port, _ := net.SplitHostPort(m.challenge)
	if err := client.Challenge.SetHTTP01Provider(http01.NewProviderServer(host, port)); err != nil {
		return nil, err
	}
	if u.reg, err = client.Registration.ResolveAccountByKey(); err != nil {
		if u.reg, err = client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true}); err != nil {
			return nil, fmt.Errorf("register: %w", err)
		}
	}
	res, err := client.Certificate.Obtain(req)
	if err != nil {
		return nil, err
	}
	if err := fsutil.WriteFileAtomic(filepath.Join(m.dir, "cert.pem"), res.Certificate, 0o600); err != nil {
		return nil, err
	}
	if err := fsutil.WriteFileAtomic(filepath.Join(m.dir, "key.pem"), res.PrivateKey, 0o600); err != nil {
		return nil, err
	}
	return m.load()
}

// order is what Let's Encrypt is asked for: the panel's domain and, when there is one,
// the subscription domain beside it — one certificate for both. It issues an IP
// certificate only with the shortlived profile and only with the IP in the SAN: an IP
// in the Common Name is refused as badCSR, so the CSR goes without one. A domain keeps
// its Common Name.
func order(id, sub string) (req certificate.ObtainRequest, noCommonName bool) {
	req = certificate.ObtainRequest{Domains: []string{id}, Bundle: true}
	if net.ParseIP(id) != nil {
		req.Profile = "shortlived"
		return req, true
	}
	if sub != "" {
		req.Domains = []string{id, sub}
	}
	return req, false
}

func (m *Manager) load() (*tls.Certificate, error) {
	c, err := tls.LoadX509KeyPair(filepath.Join(m.dir, "cert.pem"), filepath.Join(m.dir, "key.pem"))
	if err != nil {
		return nil, err
	}
	if c.Leaf == nil {
		return nil, errors.New("no leaf certificate")
	}
	return &c, nil
}

func (m *Manager) accountKey() (crypto.PrivateKey, error) {
	path := filepath.Join(m.dir, "account.key")
	if raw, err := os.ReadFile(path); err == nil {
		block, _ := pem.Decode(raw)
		if block == nil {
			return nil, errors.New("account.key: no PEM block")
		}
		return x509.ParseECPrivateKey(block.Bytes)
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

// isPrivate reports identifiers Let's Encrypt can never validate: private and loopback
// IPs, and what is not a DNS name, such as docker service names in test setups.
func isPrivate(id string) bool {
	ip := net.ParseIP(id)
	if ip == nil {
		return !hostname.Name(id) || hostname.Reserved(id)
	}
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
}

// humanError maps known failures to codes the UI translates; anything else goes out raw.
func humanError(err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "address already in use"):
		return "port80_busy"
	case strings.Contains(s, "rateLimited") || strings.Contains(s, "too many"):
		return "rate_limited"
	case strings.Contains(s, "connection") || strings.Contains(s, "timeout"):
		return "unreachable"
	}
	return s
}
