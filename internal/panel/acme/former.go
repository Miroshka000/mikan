package acme

import (
	"cmp"
	"context"
	"crypto/tls"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"mikan/internal/acmechallenge"
	"mikan/internal/panel/dnscheck"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/tlscert"
)

// Formers keeps the names the panel answered at before working (settings.KeyFormerDomains):
// the domain, or the subscription domain, it had before a change, and the address of a
// panel that had no domain until it got one. Clients keep the links they were given,
// payment providers the webhook addresses the admin entered, nodes the panel's address in
// their join keys. Each such name has a certificate of its own, served to the clients that
// name it (tlscert.BySNI): taken over from the panel's own certificates at the change, then
// renewed from the panel's CA while the name leads here. A name left without a valid
// certificate for formerGrace is let go.
type Formers struct {
	root      string // <data>/tls/former/<name>
	iss       *issuer
	set       *settings.Settings
	names     *tlscert.BySNI
	challenge *acmechallenge.Server
	// sources are where a former name's certificate is taken over from: the panel's and the
	// subscription domain's, each with the pair a change of the name put aside, and the
	// admin's own certificates.
	sources []formerSource
	log     *slog.Logger
	now     func() time.Time
	wake    chan struct{}

	mu     sync.Mutex // what follows
	state  map[string]*formerState
	onDrop func(ctx context.Context, name string)
}

type formerSource struct {
	dir    string
	custom bool // the admin's own certificate (tlscert.SaveCustom), not a pair
}

type formerState struct {
	holder *tlscert.Holder
	status FormerStatus
	next   time.Time // when it is looked at again; zero: at the next round
}

// FormerStatus is a former name as the admin panel shows it.
type FormerStatus struct {
	Name  string    `json:"name"`
	Sub   bool      `json:"sub,omitempty" doc:"Бывший домен подписок: по нему открываются только подписки"`
	Since time.Time `json:"since" doc:"Когда имя перестало быть именем панели"`
	// Kind: own, the name has a valid certificate of its own; none, it has none now.
	Kind        string     `json:"kind" enum:"own,none"`
	NotAfter    *time.Time `json:"not_after,omitempty"`
	CA          string     `json:"ca,omitempty" enum:"letsencrypt,zerossl,google"`
	Error       string     `json:"error,omitempty" doc:"Код errors.acme последнего заказа"`
	ErrorDetail string     `json:"error_detail,omitempty"`
	// DropAt: a name with no valid certificate is let go then.
	DropAt    *time.Time `json:"drop_at,omitempty" doc:"Без действующего сертификата имя уберётся само в это время"`
	CheckedAt time.Time  `json:"checked_at"`
}

// formerGrace is how long a former name may stay without a valid certificate: its own one
// ran out and cannot be renewed (the name leads elsewhere now), or it never had one.
var formerGrace = 7 * 24 * time.Hour

// formerRetry is how soon an order for a former name that failed is tried again: such a
// name mostly leads elsewhere, and every failed validation counts at the CA.
var formerRetry = 6 * time.Hour

// Formers looks after the former names with the panel's CA, accounts and port 80; names
// serves their certificates.
func (m *Manager) Formers(names *tlscert.BySNI) *Formers {
	tlsDir := filepath.Dir(m.dir)
	sub := filepath.Join(tlsDir, "sub")
	return &Formers{root: filepath.Join(tlsDir, "former"), iss: m.iss, set: m.set, names: names, challenge: m.challenge, log: m.log, now: m.now,
		wake: make(chan struct{}, 1), state: map[string]*formerState{},
		sources: []formerSource{{dir: m.dir}, {dir: filepath.Join(m.dir, prevDir)}, {dir: m.customDir, custom: true},
			{dir: filepath.Join(sub, "acme")}, {dir: filepath.Join(sub, "acme", prevDir)}, {dir: filepath.Join(sub, "custom"), custom: true}}}
}

// OnDrop sets what hears of a former name let go; the loop waits for it.
func (f *Formers) OnDrop(fn func(ctx context.Context, name string)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onDrop = fn
}

// Wake has the loop look at every former name now.
func (f *Formers) Wake() {
	f.mu.Lock()
	for _, s := range f.state {
		s.next = time.Time{}
	}
	f.mu.Unlock()
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

// Status lists the former names, the latest first.
func (f *Formers) Status() []FormerStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]FormerStatus, 0, len(f.state))
	for _, s := range f.state {
		out = append(out, s.status)
	}
	slices.SortFunc(out, func(a, b FormerStatus) int {
		if c := b.Since.Compare(a.Since); c != 0 {
			return c
		}
		return cmp.Compare(a.Name, b.Name)
	})
	return out
}

// Run looks after the former names until ctx ends.
func (f *Formers) Run(ctx context.Context) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	f.Reconcile(ctx, true)
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-f.wake:
		}
		f.Reconcile(ctx, true)
	}
}

// Reconcile follows the list as saved: a name new to it takes its certificate over at once,
// one gone from it is forgotten, and with order the names whose time has come are renewed
// or ordered. With order it is the loop's alone. Without order it needs no network: a
// change of the domain runs it before the panel's own certificate moves to the new name.
func (f *Formers) Reconcile(ctx context.Context, order bool) {
	list, err := f.set.Formers(ctx)
	if err != nil {
		f.log.Warn("acme: former names not read", "err", err)
		return
	}
	addr, err := f.set.Address(ctx)
	if err != nil {
		f.log.Warn("acme: the panel's address not read", "err", err)
		return
	}
	current := addr.Names()
	keep := map[string]settings.Former{}
	for _, e := range list {
		name := settings.FormerName(e.Name)
		if _, dup := keep[name]; name == "" || dup || slices.Contains(current, name) || isPrivate(name) {
			continue
		}
		keep[name] = e
		f.look(ctx, name, e, order)
	}
	f.mu.Lock()
	var gone []string
	for name := range f.state {
		if _, ok := keep[name]; !ok {
			gone = append(gone, name)
			delete(f.state, name)
		}
	}
	f.mu.Unlock()
	for _, name := range gone {
		if err := f.removeDir(name); err != nil {
			f.log.Warn("acme: a former name's certificate not removed", "name", name, "err", err)
		}
	}
	f.publish(addr)
}

// publish hands the names that have a certificate to the TLS server. A former address
// answers the clients that name nothing, unless the panel has no domain: those are its own.
func (f *Formers) publish(addr settings.Address) {
	out := &tlscert.Former{Certs: map[string]*tlscert.Holder{}, Sub: map[string]bool{}}
	var ipSince time.Time
	f.mu.Lock()
	for name, s := range f.state {
		if s.status.Sub {
			out.Sub[name] = true
		}
		if _, err := s.holder.Get(nil); err != nil {
			continue
		}
		key := name
		if isAddress(name) {
			if addr.Domain == "" || s.status.Since.Before(ipSince) {
				continue
			}
			key, ipSince = "", s.status.Since
		}
		out.Certs[key] = s.holder
	}
	f.mu.Unlock()
	f.names.SetFormer(out)
}

func isAddress(name string) bool {
	_, err := netip.ParseAddr(name)
	return err == nil
}

func (f *Formers) dir(name string) string { return filepath.Join(f.root, name) }

func (f *Formers) removeDir(name string) error {
	pairMu.Lock()
	defer pairMu.Unlock()
	return os.RemoveAll(f.dir(name))
}

// stateOf is the state of name, made on first use.
func (f *Formers) stateOf(name string) *formerState {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.state[name]
	if s == nil {
		s = &formerState{holder: &tlscert.Holder{}}
		f.state[name] = s
	}
	return s
}

// look serves the certificate the name has, takes one over when it has none of its own,
// and with order renews it or orders one when its time has come.
func (f *Formers) look(ctx context.Context, name string, e settings.Former, order bool) {
	s := f.stateOf(name)
	now := f.now()
	f.mu.Lock()
	due := !now.Before(s.next)
	prev := s.status
	f.mu.Unlock()
	st := FormerStatus{Name: name, Sub: e.Sub, Since: time.Unix(e.Since, 0), Kind: "none", CheckedAt: now, Error: prev.Error, ErrorDetail: prev.ErrorDetail}
	defer func() {
		f.mu.Lock()
		s.status = st
		f.mu.Unlock()
	}()
	cert, ca, err := loadPair(f.dir(name))
	if err != nil || !usable(cert, name, now) {
		if c, cca := f.takeOver(name, now); c != nil {
			cert, ca = c, cca
		}
	}
	if usable(cert, name, now) {
		s.holder.Set(cert)
		until := cert.Leaf.NotAfter
		st.Kind, st.NotAfter, st.CA = "own", &until, ca
	} else {
		s.holder.Set(nil)
	}
	if !order || !due {
		st.DropAt = dropAt(cert, name, e, now)
		return
	}
	if usable(cert, name, now) && !needsRenewal(cert.Leaf, now) {
		st.Error, st.ErrorDetail = "", ""
		f.setNext(s, now.Add(checkEvery))
		return
	}
	chosen, err := ChosenCA(ctx, f.set)
	if err != nil {
		st.Error, st.ErrorDetail = CodeUnknown, err.Error()
		f.setNext(s, now.Add(retryAfter))
		return
	}
	want := EffectiveCA(chosen, name)
	certPEM, keyPEM, err := f.iss.obtain(ctx, name, want, serverProvider{f.challenge})
	if err == nil {
		err = savePair(f.dir(name), certPEM, keyPEM, want)
	}
	if err == nil {
		if fresh, have, lerr := loadPair(f.dir(name)); lerr == nil && usable(fresh, name, f.now()) {
			s.holder.Set(fresh)
			until := fresh.Leaf.NotAfter
			st.Kind, st.NotAfter, st.CA, st.Error, st.ErrorDetail = "own", &until, have, "", ""
			f.log.Info("acme: certificate of a former name installed", "name", name, "ca", want)
			f.setNext(s, now.Add(checkEvery))
			return
		}
	}
	host, _ := f.set.String(ctx, settings.KeyPublicHost)
	p := Classify(err, dnscheck.Own(host), f.now())
	st.Error, st.ErrorDetail = p.Code, p.Detail
	f.log.Warn("acme: certificate of a former name not obtained", "name", name, "ca", want, "code", p.Code, "err", err)
	f.setNext(s, now.Add(formerRetry))
	st.DropAt = dropAt(cert, name, e, now)
	if st.DropAt != nil && !now.Before(*st.DropAt) {
		f.drop(ctx, name)
	}
}

func (f *Formers) setNext(s *formerState, at time.Time) {
	f.mu.Lock()
	s.next = at
	f.mu.Unlock()
}

// usable: cert is valid for name now.
func usable(cert *tls.Certificate, name string, now time.Time) bool {
	return cert != nil && tlscert.Covers(cert.Leaf, name) && now.Before(cert.Leaf.NotAfter)
}

// dropAt is when a name without a valid certificate is let go: formerGrace after it lost
// the last one, or after it stopped being the panel's; nil while it has one.
func dropAt(cert *tls.Certificate, name string, e settings.Former, now time.Time) *time.Time {
	if usable(cert, name, now) {
		return nil
	}
	lost := time.Unix(e.Since, 0)
	if cert != nil && tlscert.Covers(cert.Leaf, name) && cert.Leaf.NotAfter.After(lost) {
		lost = cert.Leaf.NotAfter
	}
	at := lost.Add(formerGrace)
	return &at
}

// takeOver copies the first valid certificate for name of the panel's own into the name's
// directory and returns it; nil when none covers the name.
func (f *Formers) takeOver(name string, now time.Time) (*tls.Certificate, string) {
	for _, src := range f.sources {
		var (
			c   *tls.Certificate
			ca  string
			err error
		)
		if src.custom {
			c, err = tlscert.LoadCustom(src.dir, now)
		} else {
			c, ca, err = loadPair(src.dir)
		}
		if err != nil || !usable(c, name, now) {
			continue
		}
		if src.custom {
			certPEM, keyPEM, perr := tlscert.CustomPEM(c)
			if err = perr; err == nil {
				err = savePair(f.dir(name), []byte(certPEM), []byte(keyPEM), "")
			}
		} else {
			err = copyPair(src.dir, f.dir(name))
		}
		if err != nil {
			f.log.Warn("acme: the certificate of a former name not taken over", "name", name, "err", err)
			return nil, ""
		}
		f.log.Info("acme: a former name keeps its certificate", "name", name, "from", strings.TrimPrefix(src.dir, filepath.Dir(f.root)+string(filepath.Separator)))
		if src.custom {
			ca = ""
		}
		return c, ca
	}
	return nil, ""
}

// drop lets a former name go: it leaves the saved list, its certificate is deleted.
func (f *Formers) drop(ctx context.Context, name string) {
	list, err := f.set.Formers(ctx)
	if err == nil {
		err = settings.Set(ctx, f.set, settings.KeyFormerDomains, settings.WithoutFormer(list, name))
	}
	if err != nil {
		f.log.Warn("acme: a former name not let go", "name", name, "err", err)
		return
	}
	f.log.Warn("acme: a former name is let go: no valid certificate for a week", "name", name)
	f.mu.Lock()
	delete(f.state, name)
	fn := f.onDrop
	f.mu.Unlock()
	if err := f.removeDir(name); err != nil {
		f.log.Warn("acme: a former name's certificate not removed", "name", name, "err", err)
	}
	if fn != nil {
		fn(ctx, name)
	}
}
