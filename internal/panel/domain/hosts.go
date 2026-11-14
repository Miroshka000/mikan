package domain

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"

	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/proto"
)

// The hosts of an inbound (#01) are the places its clients reach it at: an address, a port,
// a TLS name, an HTTP Host and a fingerprint, each empty (port 0) for what clients get
// without it. They go in the order of their position. Position 0 is the default host:
// subscriptions read it from the template's mikan.client, so that host is the template's,
// written only through it. Every write of a template writes it too, in the same
// transaction (syncDefaultHost); a change of the default host is a change of the template
// (UpdateHost). Every other host is one more proxy of the inbound in the subscriptions.
//
// A host also has fields of its own that no template holds (ALPN, the IP version,
// multiplexing, a CDN in front): the default host has them too, written only through the
// hosts, and a write of the template leaves them as they are.

var (
	ErrUnknownHost = errors.New("host_not_found")
	// ErrDefaultHost: the default host goes only with its inbound.
	ErrDefaultHost = errors.New("default_host")
)

// Host is a host as the admin sets it.
type Host struct {
	Address     string // "": the node's address
	Port        int    // 0: the inbound's port
	SNI         string // "": the usual TLS name
	Host        string // "": the server's HTTP Host (WebSocket, XHTTP)
	Fingerprint string // "": the panel's default uTLS profile

	// The host's own fields, each empty (false) for what clients get without it.
	ALPN        []string // what clients offer in TLS: of HostALPN, in their order
	IPVersion   string   // of HostIPVersions: how clients reach the address
	Mux         bool     // clients multiplex their connections over sing-mux
	MuxProtocol string   // of HostMuxProtocols; "": the app's own
	MuxPadding  bool
	// CDN: a CDN in front of the host shows clients its own certificate, so the node's
	// self-signed one is not to be pinned for them.
	CDN bool
}

// The values of a host's own fields, as mihomo names them.
var (
	HostALPN         = []string{"h3", "h2", "http/1.1"}
	HostIPVersions   = []string{"ipv4", "ipv6", "ipv4-prefer", "ipv6-prefer"}
	HostMuxProtocols = []string{"smux", "yamux", "h2mux"}
)

// createInbound adds an inbound with its default host.
func createInbound(ctx context.Context, q *db.Queries, p db.CreateInboundParams) (db.Inbound, error) {
	row, err := q.CreateInbound(ctx, p)
	if err != nil {
		return row, err
	}
	return row, syncDefaultHost(ctx, q, row)
}

// syncDefaultHost makes the default host of in what its template says.
func syncDefaultHost(ctx context.Context, q *db.Queries, in db.Inbound) error {
	return q.SetDefaultInboundHost(ctx, store.DefaultHost(in.ID, in.Config))
}

// Hosts lists the hosts of inbound id, the default one first.
func (s *Inbounds) Hosts(ctx context.Context, id int64) ([]db.InboundHost, error) {
	if _, err := s.inbound(ctx, s.st.Q, id); err != nil {
		return nil, err
	}
	return s.st.Q.ListInboundHosts(ctx, id)
}

// AddHost adds a host after the others of inbound id.
func (s *Inbounds) AddHost(ctx context.Context, id int64, h Host) (db.InboundHost, error) {
	h = h.trimmed()
	var row db.InboundHost
	err := s.st.Tx(ctx, func(q *db.Queries) error {
		in, err := s.inbound(ctx, q, id)
		if err != nil {
			return err
		}
		if err := checkHost(in.Config, h); err != nil {
			return err
		}
		if _, err := q.LockInbound(ctx, id); err != nil {
			return err
		}
		row, err = q.AddInboundHost(ctx, db.AddInboundHostParams{InboundID: id, Address: h.Address, Port: int64(h.Port), Sni: h.SNI, Host: h.Host, Fingerprint: h.Fingerprint,
			Alpn: h.ALPN, IpVersion: h.IPVersion, Mux: h.Mux, MuxProtocol: h.MuxProtocol, MuxPadding: h.MuxPadding, Cdn: h.CDN})
		return err
	})
	return row, err
}

// UpdateHost replaces host hostID of inbound id. The default host changes as the template
// does, through Update: the inbound's form and this one check and write the same values.
// Its own fields, which the template does not hold, go to its row in Update's transaction.
func (s *Inbounds) UpdateHost(ctx context.Context, id, hostID int64, h Host) (db.InboundHost, error) {
	h = h.trimmed()
	cur, err := s.inboundHost(ctx, s.st.Q, id, hostID)
	if err != nil {
		return db.InboundHost{}, err
	}
	if cur.Position == 0 {
		in, err := s.inbound(ctx, s.st.Q, id)
		if err != nil {
			return db.InboundHost{}, err
		}
		var cdn *proto.CDN
		if t, err := proto.Parse(in.Config); err == nil {
			cdn = proto.CDNOf(t)
		}
		if err := checkHostOptions(in.Config, h); err != nil {
			return db.InboundHost{}, err
		}
		p := InboundPatch{Client: &ClientEndpoint{Server: h.Address, Port: h.Port, SNI: h.SNI, Host: h.Host, CDN: cdn}, defaultHost: &defaultHost{id: hostID, h: h}}
		if h.Fingerprint != cur.Fingerprint {
			p.Fingerprint = &h.Fingerprint
		}
		if _, _, err := s.Update(ctx, id, p); err != nil {
			return db.InboundHost{}, hostError(err)
		}
		return s.inboundHost(ctx, s.st.Q, id, hostID)
	}
	var row db.InboundHost
	err = s.st.Tx(ctx, func(q *db.Queries) error {
		in, err := s.inbound(ctx, q, id)
		if err != nil {
			return err
		}
		if err := checkHost(in.Config, h); err != nil {
			return err
		}
		row, err = q.UpdateInboundHost(ctx, db.UpdateInboundHostParams{ID: hostID, InboundID: id, Address: h.Address, Port: int64(h.Port), Sni: h.SNI, Host: h.Host, Fingerprint: h.Fingerprint,
			Alpn: h.ALPN, IpVersion: h.IPVersion, Mux: h.Mux, MuxProtocol: h.MuxProtocol, MuxPadding: h.MuxPadding, Cdn: h.CDN})
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUnknownHost
		}
		return err
	})
	return row, err
}

// DeleteHost removes host hostID of inbound id; the default host stays (ErrDefaultHost).
func (s *Inbounds) DeleteHost(ctx context.Context, id, hostID int64) (db.InboundHost, error) {
	var row db.InboundHost
	err := s.st.Tx(ctx, func(q *db.Queries) error {
		var err error
		if row, err = s.inboundHost(ctx, q, id, hostID); err != nil {
			return err
		}
		if row.Position == 0 {
			return ErrDefaultHost
		}
		_, err = q.DeleteInboundHost(ctx, db.DeleteInboundHostParams{ID: hostID, InboundID: id})
		return err
	})
	return row, err
}

func (s *Inbounds) inbound(ctx context.Context, q *db.Queries, id int64) (db.Inbound, error) {
	in, err := q.GetInbound(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return in, ErrUnknownInbound
	}
	return in, err
}

func (s *Inbounds) inboundHost(ctx context.Context, q *db.Queries, id, hostID int64) (db.InboundHost, error) {
	if _, err := s.inbound(ctx, q, id); err != nil {
		return db.InboundHost{}, err
	}
	h, err := q.GetInboundHost(ctx, db.GetInboundHostParams{ID: hostID, InboundID: id})
	if errors.Is(err, sql.ErrNoRows) {
		return h, ErrUnknownHost
	}
	return h, err
}

// defaultHost is the default host's own fields in an InboundPatch: Update writes them to
// its row (id) in the transaction that writes the template.
type defaultHost struct {
	id int64
	h  Host
}

func (d defaultHost) write(ctx context.Context, q *db.Queries, inboundID int64) error {
	return q.SetInboundHostOptions(ctx, db.SetInboundHostOptionsParams{ID: d.id, InboundID: inboundID,
		Alpn: d.h.ALPN, IpVersion: d.h.IPVersion, Mux: d.h.Mux, MuxProtocol: d.h.MuxProtocol, MuxPadding: d.h.MuxPadding, Cdn: d.h.CDN})
}

// trimmed is h as it is saved: each ALPN once, in the order given, and no protocol or
// padding of a multiplexing that is off.
func (h Host) trimmed() Host {
	h.Address, h.SNI, h.Host, h.Fingerprint = strings.TrimSpace(h.Address), strings.TrimSpace(h.SNI), strings.TrimSpace(h.Host), strings.TrimSpace(h.Fingerprint)
	h.IPVersion, h.MuxProtocol = strings.TrimSpace(h.IPVersion), strings.TrimSpace(h.MuxProtocol)
	alpn := []string{}
	for _, a := range h.ALPN {
		if a = strings.ToLower(strings.TrimSpace(a)); a != "" && !slices.Contains(alpn, a) {
			alpn = append(alpn, a)
		}
	}
	h.ALPN = alpn
	if !h.Mux {
		h.MuxProtocol, h.MuxPadding = "", false
	}
	return h
}

// checkHost holds a host to the rules of the inbound's own client endpoint (config is its
// template): what the template's mikan.client could not hold, no host holds. A refusal is an
// *EditError named by the host's field.
func checkHost(config string, h Host) error {
	t, err := proto.Parse(config)
	if err != nil {
		return err
	}
	err = proto.SetClientEndpoint(t, h.Address, h.Port, h.SNI, h.Host)
	if err == nil && h.Fingerprint != "" {
		if !proto.UsesFingerprint(t) {
			err = &proto.Error{Code: "fingerprint_no_tls", Field: "mikan.client.fingerprint"}
		} else {
			err = proto.SetFingerprint(t, h.Fingerprint)
		}
	}
	var pe *proto.Error
	if errors.As(err, &pe) {
		return &EditError{Field: hostField(pe.Field), Err: pe}
	}
	if err != nil {
		return err
	}
	return hostOptions(t, h)
}

// HostALPNOf is the ALPN a host of t may offer: none without a TLS of the node's, over
// QUIC the listener's own (h3 when the template names none) of HostALPN, over TCP h2 and
// HTTP/1.1.
func HostALPNOf(t proto.Template) []string {
	if t.Network() != "udp" {
		// TrustTunnel picks its HTTP version itself.
		if !t.NodeCert() || t.Type() == "trusttunnel" {
			return []string{}
		}
		return []string{"h2", "http/1.1"}
	}
	own := []string{}
	if list, ok := t["alpn"].([]any); ok {
		for _, a := range list {
			if s, ok := a.(string); ok && slices.Contains(HostALPN, strings.ToLower(s)) && !slices.Contains(own, strings.ToLower(s)) {
				own = append(own, strings.ToLower(s))
			}
		}
	}
	if len(own) == 0 {
		own = []string{"h3"}
	}
	return own
}

// checkHostOptions holds the host's own fields to the inbound's template (config).
func checkHostOptions(config string, h Host) error {
	t, err := proto.Parse(config)
	if err != nil {
		return err
	}
	return hostOptions(t, h)
}

// hostOptions refuses a host's own field that clients of t could not use, as an *EditError
// named by the field. A field that a later change of the template no longer takes stays
// saved; the subscriptions are to leave it out.
func hostOptions(t proto.Template, h Host) error {
	refuse := func(field, code string) error {
		return &EditError{Field: field, Err: &proto.Error{Code: code, Field: field}}
	}
	for _, a := range h.ALPN {
		if !slices.Contains(HostALPN, a) {
			return refuse("alpn", "host_alpn")
		}
	}
	// ALPN is a TLS extension the node answers: the node certificate's protocols and those
	// on QUIC (Hysteria2, TUIC, ShadowQUIC) have it. A REALITY client speaks the target
	// site's TLS, and protocols with no TLS have none to offer it in.
	if len(h.ALPN) > 0 && len(HostALPNOf(t)) == 0 {
		return refuse("alpn", "host_alpn_no_tls")
	}
	// The node answers only the ALPN its listener has: over QUIC the template's (h3 when
	// it names none), and h3 is never a TLS-over-TCP one. A client that offers none of
	// the node's is turned away in the handshake.
	if t.Network() == "udp" {
		own := HostALPNOf(t)
		for _, a := range h.ALPN {
			if !slices.Contains(own, a) {
				return refuse("alpn", "host_alpn_quic")
			}
		}
	} else if slices.Contains(h.ALPN, "h3") {
		return refuse("alpn", "host_alpn_h3_tcp")
	} else if len(h.ALPN) > 0 {
		// WebSocket upgrades an HTTP/1.1 request and gRPC rides HTTP/2: a list without
		// that one leaves the transport nothing to run on.
		need := map[string]string{"ws": "http/1.1", "grpc": "h2"}[proto.Transport(t)]
		if need != "" && !slices.Contains(h.ALPN, need) {
			return refuse("alpn", "host_alpn_transport")
		}
	}
	if h.IPVersion != "" && !slices.Contains(HostIPVersions, h.IPVersion) {
		return refuse("ip_version", "host_ip_version")
	}
	if h.MuxProtocol != "" && !slices.Contains(HostMuxProtocols, h.MuxProtocol) {
		return refuse("mux.protocol", "host_mux_protocol")
	}
	if h.Mux {
		// sing-mux rides one TCP stream of the protocol, and the node's listeners of VLESS,
		// Trojan and Shadowsocks open it (the core's sing handler). The QUIC protocols carry
		// streams of their own, as do AnyTLS's sessions; the other TCP ones have no mux on
		// the node. XHTTP multiplexes its own requests, and Vision's flow reads the bare TLS
		// stream, which a mux inside it would hide.
		switch t.Type() {
		case "vless", "trojan", "shadowsocks":
		default:
			return refuse("mux", "host_mux_type")
		}
		if proto.Transport(t) == "xhttp" {
			return refuse("mux", "host_mux_type")
		}
		if t.Ext().Flow != "" {
			return refuse("mux", "host_mux_flow")
		}
	}
	// A CDN in front takes what an inbound behind one takes (proto.CanCDN): VLESS or Trojan
	// on the node certificate over XHTTP, WebSocket or gRPC. Its refusal says which part is
	// not; REALITY is never on the node certificate.
	if h.CDN {
		if err := proto.SetCDN(t.Clone(), &proto.CDN{}); err != nil {
			var pe *proto.Error
			if errors.As(err, &pe) {
				return refuse("cdn", pe.Code)
			}
			return err
		}
	}
	return nil
}

// hostError names a refusal of the template's client endpoint by the host's field.
func hostError(err error) error {
	var e *EditError
	if errors.As(err, &e) && (e.Field == "client" || e.Field == "fingerprint") {
		return &EditError{Field: hostField(e.Err.Field), Err: e.Err}
	}
	return err
}

// hostField is the host's field for a key of mikan.client.
func hostField(key string) string {
	switch k := strings.TrimPrefix(key, "mikan.client."); k {
	case "port", "sni", "host", "fingerprint":
		return k
	}
	return "address"
}
