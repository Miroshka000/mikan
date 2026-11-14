// Package acme obtains and renews public certificates over ACME: the panel's, and one for
// each remote node. A domain gets a normal certificate from the CA the admin chose (Let's
// Encrypt, ZeroSSL or Google Trust Services); a bare IP address a short-lived one
// (profile "shortlived") from Let's Encrypt, the only CA that certifies addresses.
// Subscription URLs and the protocols on a node's TLS are used by apps that reject
// self-signed certificates, so an IP-only install still needs a public certificate.
package acme

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"mikan/internal/acmechallenge"
	"mikan/internal/hostname"
	"mikan/internal/nodetls"
	"mikan/internal/panel/dnscheck"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/tlscert"
)

// Status is the panel's certificate as the admin panel shows it.
type Status struct {
	Kind       string    `json:"kind" enum:"self-signed,acme,custom,off" doc:"acme — выдан центром сертификации автоматически (ca), custom — свой, self-signed — временный самоподписанный, off — домен подписки не задан"`
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
	// Attempts are the latest orders at the CA, the latest first.
	Attempts []Attempt `json:"attempts,omitempty" doc:"Последние попытки получить сертификат, свежие первыми"`
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
	// journal keeps the latest orders, for the admin panel to list.
	journal *journal
	// sub: the certificate of the subscription domain (NewSub), apart from the panel's: none
	// while the domain is unset, the panel's served then. ownChallenge: the challenge server
	// is this manager's to close.
	sub          bool
	ownChallenge bool
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
		set: set, log: log, now: now, wake: make(chan struct{}, 1), challenge: acmechallenge.New(listen), customDir: filepath.Join(dataDir, "tls", "custom"),
		journal: newJournal(root), ownChallenge: true}
	m.status.Store(&Status{Kind: "self-signed", WantCA: CALetsEncrypt, CheckedAt: now()})
	return m
}

// NewSub looks after the certificate of the subscription domain (settings.KeySubDomain):
// ordered from the panel's CA, answered on the panel's port 80 (challenge), or the admin's
// own, each kept apart from the panel's in tls/sub. While the domain is unset there is
// none, and the panel's certificate is served for every name.
func NewSub(dataDir string, holder *tlscert.Holder, set *settings.Settings, challenge *acmechallenge.Server, log *slog.Logger, now func() time.Time) *Manager {
	root := filepath.Join(dataDir, "tls", "sub", "acme")
	m := &Manager{dir: root, iss: newIssuer(root, set), holder: holder, set: set, log: log, now: now, wake: make(chan struct{}, 1),
		challenge: challenge, customDir: filepath.Join(dataDir, "tls", "sub", "custom"), journal: newJournal(root), sub: true}
	m.status.Store(&Status{Kind: "off", WantCA: CALetsEncrypt, CheckedAt: now()})
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
	st.Attempts = m.journal.List()
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
		key = nodetls.PinOfDER(c.Leaf.Raw)
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
	if m.ownChallenge {
		defer m.challenge.Close()
	}
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
	if m.sub {
		return m.set.String(ctx, settings.KeySubDomain)
	}
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
		// The pair of the name the panel had stays aside: that name keeps working (Formers).
		if err = keepPrevious(m.dir, id); err == nil {
			err = savePair(m.dir, certPEM, keyPEM, ca)
		}
	}
	if err != nil {
		host, _ := m.set.String(ctx, settings.KeyPublicHost)
		p := Classify(err, dnscheck.Own(host), m.now())
		m.log.Warn("acme: certificate not obtained", "identifier", id, "ca", ca, "code", p.Code, "holder", p.Holder, "err", err)
		m.journal.Add(Attempt{At: m.now(), CA: ca, Identifier: id, Error: p.Code, Detail: p.Detail, Holder: p.Holder})
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
	m.journal.Add(Attempt{At: m.now(), CA: ca, Identifier: id})
	m.settle(ctx) // now on disk: served, unless the admin's own certificate has come in the meantime
	return false
}

// Staged is how getting a certificate ready ahead of a change of the panel's name went
// (Stage).
type Staged struct {
	// OK: the certificate is ready, or none is needed.
	OK bool
	// Ordering: the order still runs when the wait ended. It goes on, and the change finds
	// its certificate once it is there.
	Ordering bool
	// Code is a Problem's code, or custom_wrong_host: the admin's own certificate, which wins
	// while it is valid, does not cover the new name. Detail, Holder and RetryAt as Problem's.
	Code    string
	Detail  string
	Holder  string
	RetryAt *time.Time
}

// Stage gets a certificate for id ready before the panel's name becomes id, while the one
// it has is still served: a change of the domain waits for it, so that no link points at a
// name without a certificate. The certificate waits in <dir>/next until id is the panel's
// name (promote). It waits up to wait for the order. OK as well when nothing has to be
// ordered: what the panel has already covers id, or no CA certifies id.
func (m *Manager) Stage(ctx context.Context, id string, wait time.Duration) Staged {
	now := m.now()
	if isPrivate(id) {
		return Staged{OK: true}
	}
	if tlscert.HasCustom(m.customDir) {
		if cert, err := tlscert.LoadCustom(m.customDir, now); err == nil {
			if tlscert.Covers(cert.Leaf, id) {
				return Staged{OK: true}
			}
			return Staged{Code: "custom_wrong_host"}
		}
	}
	for _, dir := range []string{m.dir, filepath.Join(m.dir, nextDir)} {
		if c, _, err := loadPair(dir); err == nil && tlscert.Covers(c.Leaf, id) && !needsRenewal(c.Leaf, now) {
			return Staged{OK: true}
		}
	}
	done := make(chan Staged, 1)
	go func() { done <- m.stage(context.WithoutCancel(ctx), id) }()
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case s := <-done:
		return s
	case <-t.C:
	case <-ctx.Done():
	}
	return Staged{Ordering: true}
}

// stage orders the certificate of Stage, one order at a time with the panel's own.
func (m *Manager) stage(ctx context.Context, id string) Staged {
	m.orderMu.Lock()
	defer m.orderMu.Unlock()
	chosen, err := ChosenCA(ctx, m.set)
	if err != nil {
		return Staged{Code: CodeUnknown, Detail: err.Error()}
	}
	ca := EffectiveCA(chosen, id)
	certPEM, keyPEM, err := m.iss.obtain(ctx, id, ca, serverProvider{m.challenge})
	if err == nil {
		err = savePair(filepath.Join(m.dir, nextDir), certPEM, keyPEM, ca)
	}
	if err != nil {
		host, _ := m.set.String(ctx, settings.KeyPublicHost)
		p := Classify(err, dnscheck.Own(host), m.now())
		m.log.Warn("acme: certificate for the new name not obtained", "identifier", id, "ca", ca, "code", p.Code, "err", err)
		m.journal.Add(Attempt{At: m.now(), CA: ca, Identifier: id, Error: p.Code, Detail: p.Detail, Holder: p.Holder})
		return Staged{Code: p.Code, Detail: p.Detail, Holder: p.Holder, RetryAt: p.RetryAt}
	}
	m.log.Info("acme: certificate ready for the new name", "identifier", id, "ca", ca)
	m.journal.Add(Attempt{At: m.now(), CA: ca, Identifier: id})
	return Staged{OK: true}
}

// promote serves the certificate Stage got ready once id is the panel's name; the pair it
// replaces goes to prev. Call with m.mu held.
func (m *Manager) promote(id string, now time.Time) {
	next := filepath.Join(m.dir, nextDir)
	staged, _, err := loadPair(next)
	if err != nil || !tlscert.Covers(staged.Leaf, id) || !now.Before(staged.Leaf.NotAfter) {
		return
	}
	if cur, _, err := loadPair(m.dir); err == nil && tlscert.Covers(cur.Leaf, id) && !cur.Leaf.NotAfter.Before(staged.Leaf.NotAfter) {
		return
	}
	if err := keepPrevious(m.dir, id); err != nil {
		m.log.Warn("acme: the certificate of the former name not kept", "err", err)
		return
	}
	if err := copyPair(next, m.dir); err != nil {
		m.log.Warn("acme: the certificate ready for the new name not served", "err", err)
		return
	}
	_ = removePair(next)
	m.log.Info("acme: the certificate ready for the new name is served", "identifier", id)
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
	if m.sub && id == "" {
		// No subscription domain: nothing to order, nothing of its own to serve.
		st.Kind = "off"
		m.holder.Set(nil)
		return id, ca, false
	}
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
	m.promote(id, now)
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

// isPrivate reports identifiers a CA can never validate: an IP that is not public (a
// carrier's NAT and Tailscale's 100.64.0.0/10 too: the CA cannot reach it), and what is not
// a DNS name, such as docker service names in test setups.
func isPrivate(id string) bool { return !hostname.Public(id) }
