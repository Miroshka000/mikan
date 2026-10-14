package domain

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"net/netip"
	"strings"
	"time"

	"mikan/internal/hostname"
	"mikan/internal/panel/dnscheck"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

// The rules a node's name, host and domain follow are one for the admin panel and the shell:
// AddNode and UpdateNode apply them, and what they refuse comes back as a typed error.

var (
	// ErrChannelRequired: the fair share of a node's channel needs the channel's width.
	ErrChannelRequired = errors.New("channel_required")
)

// NameError is a node's name the rules refuse. Code is what the UI and the shell turn into
// words: name_in_use, or the code of NodeRules.Name (name_blank, name_is_group…).
type NameError struct{ Code string }

func (e *NameError) Error() string { return e.Code }

// DomainError is a node's or the panel's domain that does not lead to the machine, or could
// not be checked. Domain is the one checked. Code is domain_elsewhere, domain_not_found,
// server_ip_unknown or domain_unchecked; Value is "foreign addresses → the machine's" for the
// first; Err is the failure behind domain_unchecked, for the log.
type DomainError struct {
	Domain string
	Code   string
	Value  string
	Err    error
}

func (e *DomainError) Error() string { return e.Code }

func (e *DomainError) Unwrap() error { return e.Err }

// NodeRules are what a node is checked against besides its own fields.
type NodeRules struct {
	// Name checks the form of a node's name and that it repeats no group of the
	// subscription (subs.Groups.CheckName); the groups live in subs, which imports domain,
	// so the caller brings the check. nil asks nothing.
	Name func(name string) error
	// DNS checks that a node's domain leads to the node, over the network and so outside
	// any transaction. nil asks nothing: tests, and a panel without the check.
	DNS *dnscheck.Checker
}

// domainTimeout is how long the check of a domain against public DNS may take.
const domainTimeout = 20 * time.Second

// DomainHere checks that domain points only to own: nil when it does, or when dns is nil
// or the domain empty. Otherwise a *DomainError.
func DomainHere(ctx context.Context, dns *dnscheck.Checker, domain string, own []netip.Addr) error {
	if dns == nil || domain == "" {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, domainTimeout)
	defer cancel()
	err := dns.PointsTo(cctx, domain, own)
	var e *dnscheck.Elsewhere
	switch {
	case err == nil:
		return nil
	case errors.As(err, &e):
		return &DomainError{Domain: domain, Code: "domain_elsewhere", Value: dnscheck.Join(e.Foreign) + " → " + dnscheck.Join(own)}
	case errors.Is(err, dnscheck.ErrNotFound):
		return &DomainError{Domain: domain, Code: "domain_not_found"}
	case errors.Is(err, dnscheck.ErrUnknownServer):
		return &DomainError{Domain: domain, Code: "server_ip_unknown"}
	}
	return &DomainError{Domain: domain, Code: "domain_unchecked", Err: err}
}

// NodeAddrs is where a node is: its host when it is an IP, else what the host resolves to
// (none without dns).
func NodeAddrs(ctx context.Context, dns *dnscheck.Checker, host string) []netip.Addr {
	if ip, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		return []netip.Addr{ip.Unmap()}
	}
	if dns == nil {
		return nil
	}
	as, _ := dns.Lookup(ctx, host)
	return as
}

// checkNodeName: a node's name is its country group in Clash apps, so it follows the
// group rules and must not repeat another node (self is the node being renamed, 0 for a
// new one).
func checkNodeName(ctx context.Context, q *db.Queries, rules NodeRules, name string, self int64) error {
	if rules.Name != nil {
		if err := rules.Name(name); err != nil {
			return &NameError{Code: err.Error()}
		}
	}
	if name == "" {
		return nil
	}
	nodes, err := q.ListNodes(ctx)
	if err != nil {
		return err
	}
	for _, n := range nodes {
		if n.ID != self && strings.EqualFold(n.Name, name) {
			return &NameError{Code: "name_in_use"}
		}
	}
	return nil
}

// checkNodeDomain asks whether the node's domain leads to its host, when there is one to
// ask about.
func checkNodeDomain(ctx context.Context, rules NodeRules, domain, host string) error {
	if domain == "" {
		return nil
	}
	return DomainHere(ctx, rules.DNS, domain, NodeAddrs(ctx, rules.DNS, host))
}

// NodePatch is what UpdateNode changes; nil fields stay as they are.
type NodePatch struct {
	Name       *string
	PublicName *string
	// Host moves the node: clients and the panel's API calls go to the new address, the API
	// port stays. Domain is the node's name for its certificates and links; "" clears it.
	// Neither can be set on the panel's own node, which follows the panel's address.
	Host        *string
	Domain      *string
	Enabled     *bool
	FairShare   *bool
	ChannelMbps *int64
	Rules       NodeRules
}

// UpdateNode changes the fields p names and returns the node. Errors: ErrUnknownNode,
// ErrLocalNode (a host or domain for the panel's own node), ErrBadHost, ErrBadDomain,
// *NameError, *DomainError, ErrChannelRequired.
//
// The checks run first, the domain's one over the network included. Only what p names is
// written, over the row as it is by then and in one transaction: an edit made meanwhile
// elsewhere keeps its fields, and the shaping and the rest never land apart.
func UpdateNode(ctx context.Context, st *store.Store, id int64, p NodePatch, now time.Time) (db.Node, error) {
	cur, err := st.Q.GetNode(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return db.Node{}, ErrUnknownNode
	}
	if err != nil {
		return db.Node{}, err
	}
	trim := func(s *string) string {
		if s == nil {
			return ""
		}
		return strings.TrimSpace(*s)
	}
	name, publicName, host, dom := trim(p.Name), trim(p.PublicName), trim(p.Host), trim(p.Domain)
	if p.Name != nil {
		if err := checkNodeName(ctx, st.Q, p.Rules, name, cur.ID); err != nil {
			return db.Node{}, err
		}
	}
	if cur.Address == "" && (p.Host != nil || p.Domain != nil) {
		return db.Node{}, ErrLocalNode
	}
	if p.Host != nil {
		if !hostname.Valid(host) {
			return db.Node{}, ErrBadHost
		}
		if _, err := Readdress(cur.Address, host); err != nil {
			return db.Node{}, err
		}
	}
	if p.Domain != nil && dom != "" && !hostname.Valid(dom) {
		return db.Node{}, ErrBadDomain
	}
	// The node's domain must lead to the node: checked when it or the node's address changes.
	if p.Domain != nil || p.Host != nil {
		effDomain, effHost := cur.Domain, cur.PublicHost
		if p.Domain != nil {
			effDomain = dom
		}
		if p.Host != nil {
			effHost = host
		}
		if err := checkNodeDomain(ctx, p.Rules, effDomain, effHost); err != nil {
			return db.Node{}, err
		}
	}
	var out db.Node
	err = st.Tx(ctx, func(q *db.Queries) error {
		cur, err := q.GetNode(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUnknownNode
		}
		if err != nil {
			return err
		}
		if p.Name != nil {
			cur.Name = name
		}
		if p.PublicName != nil {
			cur.PublicName = publicName
		}
		if p.Host != nil {
			if cur.Address, err = Readdress(cur.Address, host); err != nil {
				return err
			}
			cur.PublicHost = host
		}
		if p.Domain != nil {
			cur.Domain = dom
		}
		if p.Enabled != nil {
			cur.Enabled = Flag(*p.Enabled)
		}
		if p.ChannelMbps != nil {
			cur.ChannelMbps = NullInt(p.ChannelMbps)
		}
		if p.FairShare != nil {
			cur.FairShare = Flag(*p.FairShare)
		}
		if p.FairShare != nil || p.ChannelMbps != nil {
			if cur.FairShare != 0 && !cur.ChannelMbps.Valid {
				return ErrChannelRequired
			}
			if err := q.SetNodeShaping(ctx, db.SetNodeShapingParams{FairShare: cur.FairShare, ChannelMbps: cur.ChannelMbps, UpdatedAt: now.Unix(), ID: cur.ID}); err != nil {
				return err
			}
		}
		out, err = q.UpdateNode(ctx, db.UpdateNodeParams{Name: cur.Name, Address: cur.Address, PublicHost: cur.PublicHost, Domain: cur.Domain, PublicName: cur.PublicName,
			Enabled: cur.Enabled, UpdatedAt: now.Unix(), ID: cur.ID})
		return err
	})
	return out, err
}

// Readdress is a node's API address moved to host, its port kept.
func Readdress(addr, host string) (string, error) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(host, port), nil
}
