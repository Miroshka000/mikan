package domain

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strconv"
	"strings"
	"time"

	"mikan/internal/hostname"
	"mikan/internal/nodetls"
	"mikan/internal/panel/presets"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

var (
	ErrUnknownNode = errors.New("unknown_node")
	ErrLocalNode   = errors.New("local_node")
	ErrBadHost     = errors.New("bad_host")
	ErrBadDomain   = errors.New("bad_domain")
)

// NodeInput describes a remote node the panel is going to drive.
type NodeInput struct {
	Name    string
	Host    string // public IP or name: the panel reaches the Node API here, clients connect here
	Domain  string // optional name for Hysteria2/TUIC certificates and links
	APIPort int    // 0 picks a free random port
	// PanelURL is where the node says hello (nodetls.Key.PanelURL); "" leaves it out.
	PanelURL string
	// Rules are what the name and the domain are checked against; the zero value asks
	// nothing but that the name is not taken.
	Rules NodeRules
}

// AddNode creates a remote node with the default inbounds and returns its join key.
// The key carries the node's private key: it is shown once and never stored. Errors, in
// the order they are looked for: *NameError; ErrBadHost and ErrBadDomain (both, joined,
// when both are wrong); *DomainError, the domain not leading to the host; ErrBadPort.
func AddNode(ctx context.Context, st *store.Store, panel nodetls.Pair, in NodeInput, now time.Time) (db.Node, string, error) {
	name, host, dom := strings.TrimSpace(in.Name), strings.TrimSpace(in.Host), strings.TrimSpace(in.Domain)
	if err := checkNodeName(ctx, st.Q, in.Rules, name, 0); err != nil {
		return db.Node{}, "", err
	}
	var bad error
	if !hostname.Valid(host) {
		bad = ErrBadHost
	}
	if dom != "" && !hostname.Valid(dom) {
		bad = errors.Join(bad, ErrBadDomain)
	}
	if bad != nil {
		return db.Node{}, "", bad
	}
	if err := checkNodeDomain(ctx, in.Rules, dom, host); err != nil {
		return db.Node{}, "", err
	}
	port := in.APIPort
	if port == 0 {
		n, err := rand.Int(rand.Reader, big.NewInt(40000))
		if err != nil {
			return db.Node{}, "", err
		}
		port = 20000 + int(n.Int64())
	}
	if port < 1 || port > 65535 {
		return db.Node{}, "", ErrBadPort
	}
	var node db.Node
	var key string
	err := st.Tx(ctx, func(q *db.Queries) error {
		var err error
		node, err = q.CreateNode(ctx, db.CreateNodeParams{Name: name, Address: net.JoinHostPort(host, strconv.Itoa(port)),
			PublicHost: host, Domain: dom, CreatedAt: now.Unix(), UpdatedAt: now.Unix()})
		if err != nil {
			return err
		}
		if key, err = issueKey(ctx, q, panel, node, in.PanelURL, now); err != nil {
			return err
		}
		for _, p := range presets.All {
			if !p.Default {
				continue
			}
			config, err := presets.NewConfig(p.ID, "")
			if err != nil {
				return err
			}
			if _, err := q.CreateInbound(ctx, db.CreateInboundParams{NodeID: node.ID, Name: p.Name, Preset: p.ID, Port: p.Port, Config: config,
				CreatedAt: now.Unix(), UpdatedAt: now.Unix()}); err != nil {
				return err
			}
		}
		node, err = q.GetNode(ctx, node.ID)
		return err
	})
	return node, key, err
}

// RekeyNode issues a new certificate for a remote node: the previous join key stops
// working as soon as the panel reconnects. panelURL goes into the key as in NodeInput.
func RekeyNode(ctx context.Context, st *store.Store, panel nodetls.Pair, id int64, panelURL string, now time.Time) (string, error) {
	var key string
	err := st.Tx(ctx, func(q *db.Queries) error {
		n, err := q.GetNode(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUnknownNode
		}
		if err != nil {
			return err
		}
		if n.Address == "" {
			return ErrLocalNode
		}
		key, err = issueKey(ctx, q, panel, n, panelURL, now)
		return err
	})
	return key, err
}

func issueKey(ctx context.Context, q *db.Queries, panel nodetls.Pair, n db.Node, panelURL string, now time.Time) (string, error) {
	_, portRaw, err := net.SplitHostPort(n.Address)
	if err != nil {
		return "", err
	}
	port, err := strconv.Atoi(portRaw)
	if err != nil {
		return "", err
	}
	cert, err := nodetls.Generate(fmt.Sprintf("node-%d.mikan", n.ID), x509.ExtKeyUsageServerAuth, now)
	if err != nil {
		return "", err
	}
	pin, err := nodetls.Fingerprint(cert.CertPEM)
	if err != nil {
		return "", err
	}
	panelPin, err := nodetls.Fingerprint(panel.CertPEM)
	if err != nil {
		return "", err
	}
	if err := q.SetNodeCert(ctx, db.SetNodeCertParams{CertSha256: pin, UpdatedAt: now.Unix(), ID: n.ID}); err != nil {
		return "", err
	}
	if !nodetls.ValidPanelURL(panelURL) {
		panelURL = ""
	}
	return nodetls.Key{Port: port, PanelPin: panelPin, CertPEM: cert.CertPEM, KeyPEM: cert.KeyPEM, NodeLabel: n.Name, PanelURL: panelURL, Host: NodeHost(n)}.Encode()
}

// NodeInbounds keeps the inbounds of one node.
func NodeInbounds(all []db.Inbound, nodeID int64) []db.Inbound {
	var out []db.Inbound
	for _, in := range all {
		if in.NodeID == nodeID {
			out = append(out, in)
		}
	}
	return out
}

// NodeHost is the address clients use for a remote node: its domain, else its IP.
func NodeHost(n db.Node) string {
	if n.Domain != "" {
		return n.Domain
	}
	return n.PublicHost
}
