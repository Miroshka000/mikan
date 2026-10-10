package acme

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"mikan/internal/panel/settings"
	"mikan/internal/panel/tlscert"
)

// A public certificate for each remote node, for its protocols on TLS (Hysteria2, TUIC,
// AnyTLS, TrustTunnel, VLESS TLS): clients then trust it without a pin, which sing-box apps
// cannot take and TUIC links cannot carry. The panel orders it for the node's domain, or
// its IP; the CA asks for the token on the node's port 80, which the node answers while the
// panel has it put there (PUT /v1/acme/challenge on the Node API).

// ChallengeClient is a node's challenge endpoint (nodeapi.Client).
type ChallengeClient interface {
	PresentChallenge(ctx context.Context, token, keyAuth string) error
	CleanUpChallenge(ctx context.Context, token string) error
}

// NodeTarget is a remote node the panel may order a certificate for.
type NodeTarget struct {
	ID int64
	// Host is where clients reach the node: its domain, or its IP.
	Host string
	// Own are the node's addresses: a validation that reached another one says the domain
	// points elsewhere.
	Own []netip.Addr
	// Own certificate uploaded for the node: nothing is ordered while it is there.
	HasOwn bool
	Client ChallengeClient
}

// NodeSource lists the remote nodes.
type NodeSource func(ctx context.Context) ([]NodeTarget, error)

// NodeStatus is a node's public certificate as the admin panel shows it.
type NodeStatus struct {
	Identifier  string     `json:"identifier"`
	CA          string     `json:"ca,omitempty" enum:"letsencrypt,zerossl,google" doc:"Кто выдал сертификат, который у ноды сейчас"`
	WantCA      string     `json:"ca_wanted,omitempty" enum:"letsencrypt,zerossl,google"`
	NotAfter    *time.Time `json:"not_after,omitempty"`
	Issuer      string     `json:"issuer,omitempty"`
	Names       []string   `json:"names,omitempty"`
	Error       string     `json:"error,omitempty" doc:"Код errors.acme: node_outdated — нода старее панели и не умеет получать сертификат"`
	ErrorDetail string     `json:"error_detail,omitempty"`
	Holder      string     `json:"holder,omitempty"`
	RetryAt     *time.Time `json:"retry_at,omitempty"`
	Ordering    bool       `json:"ordering,omitempty"`
	CheckedAt   *time.Time `json:"checked_at,omitempty"`
}

// ErrNoTarget: the node is not one the panel orders for (the panel's own node, a node with
// a certificate of its own, or none).
var ErrNoTarget = errors.New("node_cert_not_ordered")

type Nodes struct {
	root   string // <data>/tls/acme-nodes/<id>
	iss    *issuer
	set    *settings.Settings
	log    *slog.Logger
	now    func() time.Time
	source NodeSource
	wake   chan struct{}

	mu       sync.Mutex
	status   map[int64]NodeStatus
	attempts map[int64]*attempt
	cache    map[int64]cachedPair
	onChange func(id int64)
}

type cachedPair struct {
	mod  time.Time
	cert *tls.Certificate
	ca   string
}

// Nodes orders the remote nodes' certificates with the panel's CA and accounts; they are
// kept under <data>/tls/acme-nodes.
func (m *Manager) Nodes(source NodeSource) *Nodes {
	return &Nodes{root: filepath.Join(filepath.Dir(m.dir), "acme-nodes"), iss: m.iss, set: m.set, log: m.log, now: m.now, source: source,
		wake: make(chan struct{}, 1), status: map[int64]NodeStatus{}, attempts: map[int64]*attempt{}, cache: map[int64]cachedPair{}}
}

// OnChange sets what hears of a node's new certificate: the node gets it with its state.
func (n *Nodes) OnChange(f func(id int64)) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.onChange = f
}

func (n *Nodes) dir(id int64) string { return filepath.Join(n.root, strconv.FormatInt(id, 10)) }

// Cert is the node's public certificate when it has one valid for host; nil otherwise.
func (n *Nodes) Cert(id int64, host string) (*tls.Certificate, string) {
	dir := n.dir(id)
	fi, err := os.Stat(filepath.Join(dir, pairCert))
	if err != nil {
		return nil, ""
	}
	n.mu.Lock()
	c, ok := n.cache[id]
	n.mu.Unlock()
	if !ok || !c.mod.Equal(fi.ModTime()) {
		cert, ca, err := loadPair(dir)
		if err != nil {
			return nil, ""
		}
		c = cachedPair{mod: fi.ModTime(), cert: cert, ca: ca}
		n.mu.Lock()
		n.cache[id] = c
		n.mu.Unlock()
	}
	if !tlscert.Covers(c.cert.Leaf, host) || !n.now().Before(c.cert.Leaf.NotAfter) {
		return nil, ""
	}
	return c.cert, c.ca
}

// Status is what is known of the node's certificate; ok is false before the first look.
func (n *Nodes) Status(id int64) (NodeStatus, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	st, ok := n.status[id]
	st.Ordering = n.attempts[id] != nil
	return st, ok || st.Ordering
}

// Forget drops what the panel keeps of a deleted node.
func (n *Nodes) Forget(id int64) error {
	n.mu.Lock()
	delete(n.status, id)
	delete(n.cache, id)
	n.mu.Unlock()
	pairMu.Lock()
	defer pairMu.Unlock()
	return os.RemoveAll(n.dir(id))
}

// Wake asks the loop to look at every node now (another CA, a node added or changed).
func (n *Nodes) Wake() {
	select {
	case n.wake <- struct{}{}:
	default:
	}
}

func (n *Nodes) Run(ctx context.Context) {
	next := time.NewTimer(time.Hour)
	defer next.Stop()
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
	later(n.all(ctx))
	for {
		select {
		case <-ctx.Done():
			return
		case <-next.C:
		case <-n.wake:
		}
		later(n.all(ctx))
	}
}

// all looks after every node, one at a time; true when an order failed and is worth
// trying again soon.
func (n *Nodes) all(ctx context.Context) (failed bool) {
	targets, err := n.source(ctx)
	if err != nil {
		n.log.Warn("acme: nodes not listed", "err", err)
		return true
	}
	keep := map[int64]bool{}
	for _, t := range targets {
		keep[t.ID] = true
		if ctx.Err() != nil {
			return false
		}
		if n.attemptOnce(ctx, t) {
			failed = true
		}
	}
	n.mu.Lock()
	for id := range n.status {
		if !keep[id] {
			delete(n.status, id)
		}
	}
	n.mu.Unlock()
	return failed
}

// Renew orders the node's certificate now if it needs one and waits up to wait for the
// outcome; done is false when the order still runs then.
func (n *Nodes) Renew(ctx context.Context, id int64, wait time.Duration) (st NodeStatus, done bool, err error) {
	targets, err := n.source(ctx)
	if err != nil {
		return st, false, err
	}
	var target *NodeTarget
	for i := range targets {
		if targets[i].ID == id && !targets[i].HasOwn {
			target = &targets[i]
		}
	}
	if target == nil {
		return st, false, ErrNoTarget
	}
	finished := make(chan struct{})
	go func() {
		n.attemptOnce(context.WithoutCancel(ctx), *target)
		close(finished)
	}()
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-finished:
		done = true
	case <-t.C:
	case <-ctx.Done():
	}
	st, _ = n.Status(id)
	return st, done, nil
}

func (n *Nodes) attemptOnce(ctx context.Context, t NodeTarget) (failed bool) {
	n.mu.Lock()
	if a := n.attempts[t.ID]; a != nil {
		n.mu.Unlock()
		<-a.done
		return a.failed
	}
	a := &attempt{done: make(chan struct{})}
	n.attempts[t.ID] = a
	n.mu.Unlock()
	defer func() {
		n.mu.Lock()
		delete(n.attempts, t.ID)
		n.mu.Unlock()
		close(a.done)
	}()
	a.failed = n.ensure(ctx, t)
	return a.failed
}

// ensure orders the node's certificate when it has none valid for its host, from the CA
// chosen, or when it is due for renewal.
func (n *Nodes) ensure(ctx context.Context, t NodeTarget) (failed bool) {
	now := n.now()
	st := NodeStatus{Identifier: t.Host, CheckedAt: &now}
	defer func() {
		n.mu.Lock()
		n.status[t.ID] = st
		n.mu.Unlock()
	}()
	if t.HasOwn {
		return false
	}
	if t.Host == "" || isPrivate(t.Host) {
		st.Error = "no_public_host"
		return false
	}
	chosen, err := ChosenCA(ctx, n.set)
	if err != nil {
		st.Error, st.ErrorDetail = CodeUnknown, err.Error()
		return true
	}
	ca := EffectiveCA(chosen, t.Host)
	st.WantCA = ca
	if cert, have := n.Cert(t.ID, t.Host); cert != nil {
		n.describe(&st, cert, have)
		if !needsRenewal(cert.Leaf, now) && have == ca {
			return false
		}
	}
	if t.Client == nil {
		st.Error = CodeNodeUnreachable
		return true
	}
	certPEM, keyPEM, err := n.iss.obtain(ctx, t.Host, ca, &nodeProvider{ctx: ctx, c: t.Client})
	if err == nil {
		err = savePair(n.dir(t.ID), certPEM, keyPEM, ca)
	}
	if err != nil {
		p := Classify(err, t.Own, n.now())
		st.Error, st.ErrorDetail, st.Holder, st.RetryAt = p.Code, p.Detail, p.Holder, p.RetryAt
		if p.Code == CodeNodeOutdated {
			// Nothing to try until the node is updated: the next round is soon enough.
			n.log.Info("acme: the node predates public certificates", "node", t.ID)
			return false
		}
		n.log.Warn("acme: node certificate not obtained", "node", t.ID, "identifier", t.Host, "ca", ca, "code", p.Code, "err", err)
		return true
	}
	cert, have := n.Cert(t.ID, t.Host)
	if cert == nil {
		st.Error = CodeUnknown
		return true
	}
	n.describe(&st, cert, have)
	n.log.Info("acme: node certificate installed", "node", t.ID, "identifier", t.Host, "ca", ca)
	n.mu.Lock()
	f := n.onChange
	n.mu.Unlock()
	if f != nil {
		f(t.ID)
	}
	return false
}

func (n *Nodes) describe(st *NodeStatus, cert *tls.Certificate, ca string) {
	info := tlscert.Describe(cert, "", n.now())
	until := cert.Leaf.NotAfter
	st.CA, st.NotAfter, st.Issuer, st.Names = ca, &until, info.Issuer, info.Names
}

// nodeProvider has the node answer the challenge on its port 80.
type nodeProvider struct {
	ctx context.Context
	c   ChallengeClient
}

func (p *nodeProvider) Present(_, token, keyAuth string) error {
	return p.c.PresentChallenge(p.ctx, token, keyAuth)
}

func (p *nodeProvider) CleanUp(_, token, _ string) error {
	return p.c.CleanUpChallenge(p.ctx, token)
}
