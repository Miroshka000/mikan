package app

import (
	"context"
	"crypto/tls"
	"log/slog"
	"path/filepath"
	"strconv"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/acme"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/tlscert"
)

// NodeTLS picks the certificate a node serves on its protocols on TLS (Hysteria2, TUIC,
// AnyTLS, TrustTunnel, VLESS TLS) and the pin links carry for it:
//
//  1. the node's own certificate (Nodes → Certificate), pinned only when clients cannot
//     trust it;
//  2. a remote node's public certificate the panel ordered for it (acme.Nodes);
//  3. on the panel's own node, the panel's public certificate, for its domain or its IP:
//     the node reloads a renewed one without dropping connections, so the six-day IP
//     certificate does as well as a domain's;
//  4. a self-signed one, pinned: the panel's own for its node, one per remote node.
//
// A public certificate goes without a pin, so sing-box apps get these protocols too and
// TUIC links check the certificate.
type NodeTLS struct {
	dir   string // <data>/tls
	own   *tlscert.NodeStore
	panel *acme.Manager // nil in development
	acme  *acme.Nodes   // nil in development
	set   *settings.Settings
	log   *slog.Logger
	now   func() time.Time
}

// TLSChoice is the certificate picked for a node.
type TLSChoice struct {
	Files *nodeapi.TLSFiles
	Pin   string
	Cert  *tls.Certificate
	Kind  string // custom, acme, panel, self-signed
	CA    string // kind acme: who issued it
	Host  string // where clients reach the node
}

func NewNodeTLS(dataDir string, own *tlscert.NodeStore, panel *acme.Manager, nodes *acme.Nodes, set *settings.Settings, log *slog.Logger, now func() time.Time) *NodeTLS {
	return &NodeTLS{dir: filepath.Join(dataDir, "tls"), own: own, panel: panel, acme: nodes, set: set, log: log, now: now}
}

// Host is where clients reach the node: the panel's address for its own node.
func (x *NodeTLS) Host(ctx context.Context, n db.Node) string {
	if n.Address == "" {
		if ep, err := x.set.Endpoint(ctx); err == nil {
			return ep.Host
		}
	}
	return domain.NodeHost(n)
}

func (x *NodeTLS) Pick(n db.Node) (TLSChoice, error) {
	host := x.Host(context.Background(), n)
	c := TLSChoice{Host: host}
	use := func(cert *tls.Certificate, kind, pin string) (TLSChoice, error) {
		certPEM, keyPEM, err := tlscert.CustomPEM(cert)
		if err != nil {
			return c, err
		}
		c.Files, c.Pin, c.Cert, c.Kind = &nodeapi.TLSFiles{CertPEM: certPEM, KeyPEM: keyPEM}, pin, cert, kind
		return c, nil
	}
	if x.own != nil {
		if cert, trusted, err := x.own.Get(n.ID, host); cert != nil {
			pin := tlscert.Pin(cert)
			if trusted {
				pin = ""
			}
			return use(cert, "custom", pin)
		} else if err != nil {
			x.log.Warn("tls: a node's own certificate is not used", "node", n.ID, "err", err)
		}
	}
	if n.Address != "" && x.acme != nil {
		if cert, ca := x.acme.Cert(n.ID, host); cert != nil {
			c.CA = ca
			return use(cert, "acme", "")
		}
	}
	if n.Address == "" && x.panel != nil {
		if cert := x.panel.Public(); cert != nil && tlscert.Covers(cert.Leaf, host) {
			c.CA = x.panel.Status().CA
			return use(cert, "panel", "")
		}
	}
	dir := x.dir
	if n.Address != "" {
		dir = filepath.Join(x.dir, "nodes", strconv.FormatInt(n.ID, 10))
		if _, err := tlscert.LoadOrCreateSelfSigned(dir, domain.NodeHost(n), x.now()); err != nil {
			return c, err
		}
	}
	certPEM, keyPEM, pin, err := tlscert.PEM(dir)
	if err != nil {
		return c, err
	}
	c.Files, c.Pin, c.Kind = &nodeapi.TLSFiles{CertPEM: certPEM, KeyPEM: keyPEM}, pin, "self-signed"
	if cert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM)); err == nil {
		c.Cert = &cert
	}
	return c, nil
}

// Source is what the node's syncer sends: the files and their pin.
func (x *NodeTLS) Source(n db.Node) func() (*nodeapi.TLSFiles, string, error) {
	return func() (*nodeapi.TLSFiles, string, error) {
		c, err := x.Pick(n)
		return c.Files, c.Pin, err
	}
}
