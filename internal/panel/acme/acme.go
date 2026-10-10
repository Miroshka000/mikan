// Package acme obtains and renews public certificates over ACME: the panel's, and one for
// each remote node. A domain gets a normal certificate from the CA the admin chose (Let's
// Encrypt, ZeroSSL or Google Trust Services); a bare IP address a short-lived one
// (profile "shortlived") from Let's Encrypt, the only CA that certifies addresses.
// Subscription URLs and the protocols on a node's TLS are used by apps that reject
// self-signed certificates, so an IP-only install still needs a public certificate.
package acme

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"mikan/internal/acmechallenge"
	"mikan/internal/hostname"
	"mikan/internal/panel/dnscheck"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/tlscert"
)

// Status is the panel's certificate as the admin panel shows it.
type Status struct {
	Kind       string    `json:"kind" enum:"self-signed,acme,custom" doc:"acme — выдан центром сертификации автоматически (ca), custom — свой, self-signed — временный самоподписанный"`
	Identifier string    `json:"identifier"`
	NotAfter   time.Time `json:"not_after"`
	// CA issued the certificate served (kind acme); WantCA is the one the next order goes to:
	// they differ while a change of the CA is under way.
	CA     string `json:"ca,omitempty" enum:"letsencrypt,zerossl,google"`
	WantCA string `json:"ca_wanted" enum:"letsencrypt,zerossl,google" doc:"Куда уйдёт следующий заказ: выбранный центр, для IP всегда Let's Encrypt"`
	// Error is a code of errors.acme; ErrorDetail the CA's or the system's own words.
	Error       string     `json:"error,omitempty"`
	ErrorDetail string     `json:"error_detail,omitempty" doc:"Подробности ошибки как есть, для «подробнее»"`
	Holder      string     `json:"holder,omitempty" doc:"Кто держит порт 80 (port80_busy), если это видно"`
	RetryAt     *time.Time `json:"retry_at,omitempty" doc:"rate_limited: когда центр снова примет заказ"`
	Ordering    bool       `json:"ordering,omitempty" doc:"Заказ идёт прямо сейчас"`
	CheckedAt   time.Time  `json:"checked_at"`
	// The certificate served (kinds acme and custom).
	Issuer  string   `json:"issuer,omitempty"`
	Names   []string `json:"names,omitempty"`
	Trusted bool     `json:"trusted,omitempty" doc:"Сертификат публично доверенный для адреса панели"`
}

type Manager struct {
	dir      string
	iss      *issuer
	holder   *tlscert.Holder
	fallback *tls.Certificate
	set      *settings.Settings
	log      *slog.Logger
	now      func() time.Time
	mu       sync.Mutex // the state below and the serving certificate; never held across an order
	orderMu  sync.Mutex // one order at a time: it takes minutes when port 80 hangs
	status   atomic.Pointer[Status]
	wake     chan struct{}
	// challenge answers HTTP-01 on MIKAN_ACME_LISTEN (":80" by default), for orders and for
	// the port 80 check.
	challenge *acmechallenge.Server
	// The attempt under way, which a second caller joins instead of ordering again.
	attMu sync.Mutex
	cur   *attempt
	// customDir holds the admin's own certificate (tlscert.SaveCustom); it wins over
	// the CA while valid. customMod is when its files last changed, as ensure saw.
	customDir string
	customMod time.Time
	// onChange hears of every change of Public: the local node gets that certificate with
	// its state, which is sent again only on a change. announced is the last one told.
	annMu     sync.Mutex
	onChange  func()
	announced string
}

type attempt struct {
	done   chan struct{}
	failed bool
}

func New(dataDir string, holder *tlscert.Holder, fallback *tls.Certificate, set *settings.Settings, log *slog.Logger, now func() time.Time) *Manager {
	listen, err := acmechallenge.ParseListen(os.Getenv("MIKAN_ACME_LISTEN"))
	if err != nil {
		log.Warn("MIKAN_ACME_LISTEN is not host:port: the HTTP-01 challenge is answered on "+acmechallenge.DefaultListen, "err", err)
		listen = acmechallenge.DefaultListen
	}
	root := filepath.Join(dataDir, "tls", "acme")
	m := &Manager{dir: root, iss: newIssuer(root, set), holder: holder, fallback: fallback,
		set: set, log: log, now: now, wake: make(chan struct{}, 1), challenge: acmechallenge.New(listen), customDir: filepath.Join(dataDir, "tls", "custom")}
	m.status.Store(&Status{Kind: "self-signed", WantCA: CALetsEncrypt, CheckedAt: now()})
	return m
}

// Challenge is the panel's HTTP-01 responder: the certificate check puts a token of its own
// on it to see that port 80 reaches the panel from outside.
func (m *Manager) Challenge() *acmechallenge.Server { return m.challenge }

// Status is the certificate now, with Ordering set while an order runs.
func (m *Manager) Status() Status {
	st := *m.status.Load()
	m.attMu.Lock()
	st.Ordering = m.cur != nil
	m.attMu.Unlock()
	return st
}

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
// from the CA, or the admin's own when it is publicly trusted and covers the address.
func (m *Manager) Trusted() bool {
	st := m.status.Load()
	if !st.NotAfter.IsZero() && !m.now().Before(st.NotAfter) {
		return false
	}
	switch st.Kind {
	case "acme":
		return true
	case "custom":
		return st.Trusted && st.Error == ""
	}
	return false
}

// Public is the certificate the panel serves when clients trust it without a pin: the one
// from the CA, or the admin's own that is publicly trusted. nil otherwise, and once expired.
func (m *Manager) Public() *tls.Certificate {
	if !m.Trusted() {
		return nil
	}
	c, err := m.holder.Get(nil)
	if err != nil || c.Leaf == nil || !m.now().Before(c.Leaf.NotAfter) {
		return nil
	}
	return c
}

// Served is the leaf the panel serves now, whatever it is; nil before any.
func (m *Manager) Served() *x509.Certificate {
	c, err := m.holder.Get(nil)
	if err != nil {
		return nil
	}
	return c.Leaf
}

// Renew asks the background loop to try again now (a new domain, another CA, kill -HUP).
func (m *Manager) Renew() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// RenewNow tries now and waits up to wait for the outcome: the admin's "Request now". done
// is false when the order still runs then; it goes on in the background.
func (m *Manager) RenewNow(ctx context.Context, wait time.Duration) (st Status, done bool) {
	finished := make(chan struct{})
	go func() {
		m.attemptOnce(context.WithoutCancel(ctx))
		close(finished)
	}()
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-finished:
		return m.Status(), true
	case <-t.C:
	case <-ctx.Done():
	}
	return m.Status(), false
}

// attemptOnce runs ensure, or waits for the one under way and shares its outcome.
func (m *Manager) attemptOnce(ctx context.Context) (failed bool) {
	m.attMu.Lock()
	if a := m.cur; a != nil {
		m.attMu.Unlock()
		<-a.done
		return a.failed
	}
	a := &attempt{done: make(chan struct{})}
	m.cur = a
	m.attMu.Unlock()
	defer func() {
		m.attMu.Lock()
		m.cur = nil
		m.attMu.Unlock()
		close(a.done)
	}()
	a.failed = m.ensure(ctx)
	return a.failed
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
	defer m.challenge.Close()
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
	later(m.attemptOnce(ctx))
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
		later(m.attemptOnce(ctx))
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

// ClearCustom goes back to the CA or the self-signed certificate. The custom one is served
// until the loop has the other: there is no gap.
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

// ensure makes the panel serve the right certificate and, when it needs a new one from
// the CA, orders it. It reports whether that order failed. The order runs outside m.mu:
// uploading a certificate of one's own must not wait for it.
func (m *Manager) ensure(ctx context.Context) (orderFailed bool) {
	id, ca, need := m.settle(ctx)
	if !need {
		return false
	}
	m.orderMu.Lock()
	defer m.orderMu.Unlock()
	// Checked again: a certificate may have come meanwhile, an order that waited for ours.
	if id, ca, need = m.settle(ctx); !need {
		return false
	}
	certPEM, keyPEM, err := m.iss.obtain(ctx, id, ca, serverProvider{m.challenge})
	if err == nil {
		err = savePair(m.dir, certPEM, keyPEM, ca)
	}
	if err != nil {
		host, _ := m.set.String(ctx, settings.KeyPublicHost)
		p := Classify(err, dnscheck.Own(host), m.now())
		m.log.Warn("acme: certificate not obtained", "identifier", id, "ca", ca, "code", p.Code, "err", err)
		m.mu.Lock()
		st := *m.status.Load()
		if st.Kind == "custom" {
			// The admin's own certificate came while the order ran (SetCustom settled it into
			// the status): nothing is wrong.
			m.mu.Unlock()
			return false
		}
		if st.Error == "" { // a custom certificate that is broken is the thing to tell the admin of
			st.Error, st.ErrorDetail, st.Holder, st.RetryAt = p.Code, p.Detail, p.Holder, p.RetryAt
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
	m.log.Info("acme: certificate installed", "identifier", id, "ca", ca)
	m.settle(ctx) // now on disk: served, unless the admin's own certificate has come in the meantime
	return false
}

// settle serves the best certificate there already is and says whether a new one has to
// be ordered for id, from ca. It does no network.
func (m *Manager) settle(ctx context.Context) (id, ca string, order bool) {
	defer m.announce() // after the lock and the status: Public reads both
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	id, err := m.identifier(ctx)
	st := &Status{Kind: "self-signed", Identifier: id, CheckedAt: now}
	defer func() { m.status.Store(st) }()
	if err != nil {
		st.Error, st.ErrorDetail = CodeUnknown, err.Error()
		return id, "", false
	}
	chosen, err := ChosenCA(ctx, m.set)
	if err != nil {
		st.Error, st.ErrorDetail = CodeUnknown, err.Error()
		return id, "", false
	}
	ca = EffectiveCA(chosen, id)
	st.WantCA = ca
	// The admin's own certificate wins while it is valid; an expired or broken one falls
	// back to the rest, and the status says why.
	m.customMod = tlscert.CustomModTime(m.customDir)
	if tlscert.HasCustom(m.customDir) {
		cert, err := tlscert.LoadCustom(m.customDir, now)
		if err == nil {
			info := tlscert.Describe(cert, id, now)
			m.holder.Set(cert)
			st.Kind, st.NotAfter, st.Issuer, st.Names, st.Trusted = "custom", cert.Leaf.NotAfter, info.Issuer, info.Names, info.Trusted
			if id != "" && !tlscert.Covers(cert.Leaf, id) {
				st.Error = "custom_wrong_host"
			}
			return id, ca, false
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
		return id, ca, false
	}
	if cert, have, err := loadPair(m.dir); err == nil && tlscert.Covers(cert.Leaf, id) && now.Before(cert.Leaf.NotAfter) {
		m.holder.Set(cert) // kept serving while it is renewed, or ordered from another CA
		info := tlscert.Describe(cert, id, now)
		st.Kind, st.NotAfter, st.CA, st.Issuer, st.Names, st.Trusted = "acme", cert.Leaf.NotAfter, have, info.Issuer, info.Names, true
		if !needsRenewal(cert.Leaf, now) && have == ca {
			return id, ca, false
		}
	}
	if st.Kind == "self-signed" {
		m.useFallback()
	}
	return id, ca, true
}

func (m *Manager) useFallback() {
	if m.fallback != nil {
		m.holder.Set(m.fallback)
	}
}

// isPrivate reports identifiers a CA can never validate: private and loopback IPs, and
// what is not a DNS name, such as docker service names in test setups.
func isPrivate(id string) bool {
	ip := net.ParseIP(id)
	if ip == nil {
		return !hostname.Name(id) || hostname.Reserved(id)
	}
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
}
