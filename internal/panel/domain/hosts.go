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
// multiplexing, a CDN in front, its name, more addresses and TLS names, who it is shown to): the default host has them too,
// written only through the hosts, and a write of the template leaves them as they are.

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
	// Name is the host's proxy name in subscriptions, with the variables of the profile
	// title filled for each user; "": the inbound's, numbered after the default host's.
	Name string
	// Addresses and SNIs are more of Address and SNI: each client gets one of the host's
	// own and these, always the same one (by its token). At most HostChoicesMax each.
	Addresses []string
	SNIs      []string
	// States are the states of the subscriptions the host is shown to, of HostStates (an
	// expiring one is active); none: every one. Trial is of HostTrials: only to the trial's
	// subscriptions, never to them, or "" to both. A user not shown it does not get it.
	States []string
	Trial  string
	// GroupMode and Groups say who it is shown to by group (HostShowsGroup): of
	// HostGroupModes, to the clients of Groups alone or to every client but theirs; "": to
	// every client, and no Groups.
	GroupMode string
	Groups    []int64
}

// HostChoicesMax bounds a host's more addresses and TLS names each.
const HostChoicesMax = 16

// The values of a host's own fields, as mihomo names them.
var (
	HostALPN         = []string{"h3", "h2", "http/1.1"}
	HostIPVersions   = []string{"ipv4", "ipv6", "ipv4-prefer", "ipv6-prefer"}
	HostMuxProtocols = []string{"smux", "yamux", "h2mux"}
	HostStates       = []string{StateActive, StateLimited, StateExpired, StateDisabled}
	HostTrials       = []string{"only", "never"}
)

// HostShows says whether a host of states and trial goes to a user in state (State's; an
// expiring user is an active one) on the trial or not.
func HostShows(states []string, trialOf string, state string, trial bool) bool {
	if state == StateExpiring {
		state = StateActive
	}
	if len(states) > 0 && !slices.Contains(states, state) {
		return false
	}
	return trialOf == "" || trialOf == "only" && trial || trialOf == "never" && !trial
}

// HostForSome says whether a host of states, trial and group mode goes to some users only
// (HostShows, HostShowsGroup): not to every state, to the trial's or the others' alone, or by
// group.
func HostForSome(states []string, trialOf, groupMode string) bool {
	if trialOf != "" || groupMode != "" {
		return true
	}
	return len(states) > 0 && slices.ContainsFunc(HostStates, func(s string) bool { return !slices.Contains(states, s) })
}

// OnTrial says whether u is on the trial: it came of the trial (users.source) and paid for
// nothing besides it. A trial the admin lengthened is still one; a user who bought a plan or
// more traffic since is not.
func OnTrial(ctx context.Context, q *db.Queries, u db.User) (bool, error) {
	if u.Source != UserFromTrial {
		return false, nil
	}
	paid, err := q.UserPaidBeyondTrial(ctx, u.ID)
	return !paid, err
}

// createInbound adds an inbound with its default host, which takes the place of its node's
// last server in the subscriptions.
func createInbound(ctx context.Context, q *db.Queries, p db.CreateInboundParams) (db.Inbound, error) {
	if err := q.LockSubOrder(ctx); err != nil {
		return db.Inbound{}, err
	}
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
		// The groups before the inbound: a group's deletion takes its row, then the hosts'.
		if err := LockGroups(ctx, q, "groups", h.Groups...); err != nil {
			return err
		}
		if _, err := q.LockInbound(ctx, id); err != nil {
			return err
		}
		// It takes its inbound's last place in the subscriptions, as the order is.
		if err := q.LockSubOrder(ctx); err != nil {
			return err
		}
		row, err = q.AddInboundHost(ctx, db.AddInboundHostParams{InboundID: id, Address: h.Address, Port: int64(h.Port), Sni: h.SNI, Host: h.Host, Fingerprint: h.Fingerprint,
			Alpn: h.ALPN, IpVersion: h.IPVersion, Mux: h.Mux, MuxProtocol: h.MuxProtocol, MuxPadding: h.MuxPadding, Cdn: h.CDN, Name: h.Name, Addresses: h.Addresses, Snis: h.SNIs, States: h.States, Trial: h.Trial,
			GroupMode: h.GroupMode, Groups: h.Groups})
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
		if err := LockGroups(ctx, q, "groups", h.Groups...); err != nil {
			return err
		}
		row, err = q.UpdateInboundHost(ctx, db.UpdateInboundHostParams{ID: hostID, InboundID: id, Address: h.Address, Port: int64(h.Port), Sni: h.SNI, Host: h.Host, Fingerprint: h.Fingerprint,
			Alpn: h.ALPN, IpVersion: h.IPVersion, Mux: h.Mux, MuxProtocol: h.MuxProtocol, MuxPadding: h.MuxPadding, Cdn: h.CDN, Name: h.Name, Addresses: h.Addresses, Snis: h.SNIs, States: h.States, Trial: h.Trial,
			GroupMode: h.GroupMode, Groups: h.Groups})
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

// write writes the fields; the groups they name are locked first in the transaction (lock).
func (d defaultHost) write(ctx context.Context, q *db.Queries, inboundID int64) error {
	return q.SetInboundHostOptions(ctx, db.SetInboundHostOptionsParams{ID: d.id, InboundID: inboundID,
		Alpn: d.h.ALPN, IpVersion: d.h.IPVersion, Mux: d.h.Mux, MuxProtocol: d.h.MuxProtocol, MuxPadding: d.h.MuxPadding, Cdn: d.h.CDN, Name: d.h.Name, Addresses: d.h.Addresses, Snis: d.h.SNIs, States: d.h.States, Trial: d.h.Trial,
		GroupMode: d.h.GroupMode, Groups: d.h.Groups})
}

// lock keeps the groups the fields name until the transaction ends: first in it, as a group's
// deletion takes its row before the hosts'.
func (d defaultHost) lock(ctx context.Context, q *db.Queries) error {
	return LockGroups(ctx, q, "groups", d.h.Groups...)
}

// trimmed is h as it is saved: each ALPN once, in the order given, and no protocol or
// padding of a multiplexing that is off.
func (h Host) trimmed() Host {
	h.Address, h.SNI, h.Host, h.Fingerprint = strings.TrimSpace(h.Address), strings.TrimSpace(h.SNI), strings.TrimSpace(h.Host), strings.TrimSpace(h.Fingerprint)
	h.Name = strings.TrimSpace(h.Name)
	h.Addresses, h.SNIs = choices(h.Addresses, h.Address), choices(h.SNIs, h.SNI)
	h.IPVersion, h.MuxProtocol = strings.TrimSpace(h.IPVersion), strings.TrimSpace(h.MuxProtocol)
	// The states in HostStates' order, each once; an unknown one stays for hostOptions to refuse.
	states := []string{}
	for _, st := range HostStates {
		if slices.Contains(h.States, st) {
			states = append(states, st)
		}
	}
	for _, st := range h.States {
		if !slices.Contains(states, st) {
			states = append(states, st)
		}
	}
	// Every state is no state at all: the host goes to everyone.
	if len(states) == len(HostStates) && !slices.ContainsFunc(states, func(st string) bool { return !slices.Contains(HostStates, st) }) {
		states = []string{}
	}
	h.States, h.Trial = states, strings.TrimSpace(h.Trial)
	h.GroupMode = strings.TrimSpace(h.GroupMode)
	h.Groups = hostGroups(h.GroupMode, h.Groups)
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

// choices is a list of more values as it is saved: trimmed, each once, none empty or the
// host's own (one).
func choices(list []string, one string) []string {
	out := []string{}
	for _, v := range list {
		if v = strings.TrimSpace(v); v != "" && !strings.EqualFold(v, one) && !slices.ContainsFunc(out, func(x string) bool { return strings.EqualFold(x, v) }) {
			out = append(out, v)
		}
	}
	return out
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
	list, _ := t["alpn"].([]any)
	if len(list) == 0 {
		return []string{"h3"}
	}
	// A listener of ALPN of its own answers only those: one the panel cannot offer leaves
	// the host none.
	own := []string{}
	for _, a := range list {
		if s, ok := a.(string); ok && slices.Contains(HostALPN, strings.ToLower(s)) && !slices.Contains(own, strings.ToLower(s)) {
			own = append(own, strings.ToLower(s))
		}
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
	refuse := hostRefusal
	// The more addresses and names are held to the rules of the host's own.
	if len(h.Addresses) > HostChoicesMax {
		return refuse("addresses", "host_choices_long")
	}
	if len(h.SNIs) > HostChoicesMax {
		return refuse("snis", "host_choices_long")
	}
	for _, a := range h.Addresses {
		if err := proto.SetClientEndpoint(t.Clone(), a, 0, "", ""); err != nil {
			return refuse("addresses", "config_client_server")
		}
	}
	for _, n := range h.SNIs {
		if err := proto.SetClientEndpoint(t.Clone(), "", 0, n, ""); err != nil {
			return refuse("snis", "config_client_sni")
		}
	}
	for _, st := range h.States {
		if !slices.Contains(HostStates, st) {
			return refuse("states", "host_state")
		}
	}
	if h.Trial != "" && !slices.Contains(HostTrials, h.Trial) {
		return refuse("trial", "host_trial")
	}
	if err := checkHostGroups(h.GroupMode, h.Groups); err != nil {
		return err
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

// hostRefusal refuses a host's own field as an *EditError named by the field.
func hostRefusal(field, code string) error {
	return &EditError{Field: field, Err: &proto.Error{Code: code, Field: field}}
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
