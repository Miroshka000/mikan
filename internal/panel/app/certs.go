package app

import (
	"context"
	"net/netip"
	"strconv"
	"time"

	"mikan/internal/panel/acme"
	"mikan/internal/panel/api"
	"mikan/internal/panel/certcheck"
	"mikan/internal/panel/dnscheck"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/nodesync"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/tlscert"
	"mikan/internal/proto"
)

// nodeTLSView is what the node card says of the certificate on the node's protocols on TLS.
func nodeTLSView(x *NodeTLS, nodes *nodesync.Manager, orders *acme.Nodes, now func() time.Time) func(ctx context.Context, n db.Node) *api.NodeTLSView {
	return func(ctx context.Context, n db.Node) *api.NodeTLSView {
		c, err := x.Pick(n)
		if err != nil {
			return nil
		}
		v := &api.NodeTLSView{Kind: c.Kind, Pinned: c.Pin != "", CA: c.CA}
		// Links pin what the node serves: until it took a new certificate, the old one.
		if nodes != nil {
			if pin, ok := nodes.ServedPin(n.ID); ok {
				v.Pinned = pin != ""
			}
		}
		if c.Cert != nil && c.Cert.Leaf != nil {
			info := tlscert.Describe(c.Cert, "", now())
			until := c.Cert.Leaf.NotAfter
			v.Issuer, v.Names, v.NotAfter = info.Issuer, info.Names, &until
		}
		if n.Address != "" && orders != nil && c.Kind != "custom" {
			st, _ := orders.Status(n.ID)
			v.ACME = &st
		}
		return v
	}
}

// checkCerts gathers what «Проверить сертификат» looks at and runs it.
func checkCerts(st *store.Store, set *settings.Settings, certs *acme.Manager, x *NodeTLS, nodes *nodesync.Manager, orders *acme.Nodes, dns *dnscheck.Checker) func(ctx context.Context) (certcheck.CertReport, error) {
	checker := certcheck.New(dns)
	return func(ctx context.Context) (certcheck.CertReport, error) {
		ep, err := set.Endpoint(ctx)
		if err != nil {
			return certcheck.CertReport{}, err
		}
		dom, err := set.String(ctx, settings.KeyDomain)
		if err != nil {
			return certcheck.CertReport{}, err
		}
		public, err := set.String(ctx, settings.KeyPublicHost)
		if err != nil {
			return certcheck.CertReport{}, err
		}
		sub, _, err := settings.Get[int](ctx, set, settings.KeySubPort)
		if err != nil {
			return certcheck.CertReport{}, err
		}
		in := certcheck.Input{Domain: dom, Host: ep.Host, Ports: []int{ep.Port, sub}, Own: dnscheck.Own(public),
			Expected: certs.Served(), Status: certs.Status(), Challenge: certs.Challenge()}
		all, err := st.Q.ListNodes(ctx)
		if err != nil {
			return certcheck.CertReport{}, err
		}
		inbounds, err := st.Q.ListInbounds(ctx)
		if err != nil {
			return certcheck.CertReport{}, err
		}
		for _, n := range all {
			if n.Enabled == 0 {
				continue
			}
			c, err := x.Pick(n)
			if err != nil {
				continue
			}
			cn := certcheck.Node{ID: n.ID, Name: n.Name, Local: n.Address == "", Host: c.Host, Kind: c.Kind, Pinned: c.Pin != ""}
			if _, err := netip.ParseAddr(c.Host); err != nil {
				cn.SNI = c.Host
			}
			if nodes != nil {
				if pin, ok := nodes.ServedPin(n.ID); ok {
					cn.Pinned = pin != ""
				}
			}
			if c.Cert != nil {
				cn.Expected = c.Cert.Leaf
			}
			if n.Address != "" && orders != nil {
				if s, ok := orders.Status(n.ID); ok {
					cn.ACME = &s
				}
			}
			for _, in := range domain.NodeInbounds(inbounds, n.ID) {
				t, err := proto.Parse(in.Config)
				if in.Enabled == 0 || err != nil || !t.NodeCert() || t.Network() != "tcp" {
					continue
				}
				if p, err := strconv.Atoi(in.Port); err == nil {
					cn.Ports = append(cn.Ports, certcheck.Port{Inbound: in.Name, Port: p})
				}
			}
			in.Nodes = append(in.Nodes, cn)
		}
		return checker.Run(ctx, in), nil
	}
}
