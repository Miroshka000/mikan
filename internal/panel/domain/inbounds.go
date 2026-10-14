package domain

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/presets"
	"mikan/internal/panel/secure"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/proto"
)

var (
	ErrUnknownPreset  = errors.New("unknown_preset")
	ErrUnknownInbound = errors.New("unknown_inbound")
	ErrBadPort        = errors.New("bad_port")
	ErrBadListen      = errors.New("bad_listen")
	ErrAutoPortListen = errors.New("auto_port_listen") // a proxy in front would not learn the new port
	ErrAutoSNIListen  = errors.New("auto_sni_listen")  // nor the new site, if it routes by SNI
	ErrUnknownPool    = errors.New("pool_not_found")
	// ErrInboundChanged: someone else kept changing the inbound while the change was
	// checked; nothing was written, the caller may try again.
	ErrInboundChanged = errors.New("inbound_changed")
)

// errStale: the inbound differs from the row the checks of this attempt ran on.
var errStale = errors.New("inbound changed under the checks")

// updateTries is how often Update checks again on a row that changed under it.
const updateTries = 3

// EditError is a form field the inbound's template does not take. Field is the field
// ("dest", "fingerprint", "obfs", "client"); Err is the template's reason, its Field
// naming the part ("mikan.client.sni").
type EditError struct {
	Field string
	Err   *proto.Error
}

func (e *EditError) Error() string { return e.Field + ": " + e.Err.Error() }
func (e *EditError) Unwrap() error { return e.Err }

// NameInUseError: another inbound of the node already shows under the name in
// subscriptions.
type NameInUseError struct{ Owner string }

func (e *NameInUseError) Error() string { return "name_in_use: " + e.Owner }

// DryRun runs a listener through the mihomo of the node that is going to run it.
type DryRun interface {
	Validate(ctx context.Context, nodeID int64, req nodeapi.ValidateRequest) error
}

// Inbounds adds and changes inbounds for the admin API, the server CLI and the automatic
// moves: one set of rules, and one transaction per change, so a change refused on any
// field leaves the inbound as it was. Audit entries and change notices stay with the
// callers.
type Inbounds struct {
	st      *store.Store
	dry     DryRun
	now     func() time.Time
	resolve func(ctx context.Context, host string) ([]netip.Addr, error) // nil: names are not looked up
	host    HostLookup                                                   // nil: what listens on the servers is not known
}

// NewInbounds: dry nil skips the nodes' own check, for callers that do not talk to the
// nodes (the CLI, the automatic moves).
func NewInbounds(st *store.Store, dry DryRun, now func() time.Time) *Inbounds {
	return &Inbounds{st: st, dry: dry, now: now}
}

// NewInbound is an inbound to add.
type NewInbound struct {
	NodeID int64  // 0: the panel's own node
	Preset string // presets.Custom takes Config
	Port   string // "": the preset's
	Dest   string // a REALITY preset's target; "": the default
	Config string // the template of presets.Custom
}

// InboundPatch changes an inbound; a nil field stays as it is.
type InboundPatch struct {
	// What clients get: a change here bumps updated_at, so their profiles refresh.
	Port        *string
	Enabled     *bool
	Config      *string // the whole template
	Dest        *string // the REALITY target, host:port
	ServerName  *string // the name clients send with Dest; "" takes the host of Dest
	Fingerprint *string
	Obfs        *string
	XHTTP       *proto.XHTTPTuning // the whole XHTTP tuning, as the form shows it
	Client      *ClientEndpoint    // replaces all three
	DisplayName *string
	// What only the node uses.
	Listen     *string
	AutoPort   *bool
	AutoSNI    *bool
	Outbound   *string // direct, warp or node
	ExitNodeID *int64  // for Outbound node
	PoolID     *int64  // 0: the main traffic

	// FromPort, when set, applies the patch only while the inbound is on this port: an
	// automatic move decided on what a node reported never lands on a row moved since
	// (ErrInboundChanged).
	FromPort *string
}

// EditsTemplate: the patch changes the listener's template.
func (p InboundPatch) EditsTemplate() bool {
	return p.Config != nil || p.Dest != nil || p.Fingerprint != nil || p.Obfs != nil || p.XHTTP != nil || p.Client != nil
}

// ForClients: the patch changes what clients get.
func (p InboundPatch) ForClients() bool {
	return p.EditsTemplate() || p.Port != nil || p.Enabled != nil || p.DisplayName != nil
}

// ClientEndpoint is where clients connect when a TCP proxy or a CDN stands in front of
// the node; empty values keep the node's address, the inbound's port and SNI.
type ClientEndpoint struct {
	Server string
	Port   int
	SNI    string
}

// Create adds an inbound: a preset's with fresh keys, or a custom template.
func (s *Inbounds) Create(ctx context.Context, in NewInbound) (db.Inbound, error) {
	info, ok := presets.Get(in.Preset)
	if !ok {
		return db.Inbound{}, ErrUnknownPreset
	}
	node, err := s.node(ctx, in.NodeID)
	if err != nil {
		return db.Inbound{}, err
	}
	port := in.Port
	if port == "" {
		port = info.Port
	}
	if !ValidPort(port) {
		return db.Inbound{}, ErrBadPort
	}
	config := in.Config
	if info.ID != presets.Custom {
		if config, err = presets.NewConfig(info.ID, in.Dest); err != nil {
			return db.Inbound{}, err
		}
	}
	t, err := s.CheckTemplate(ctx, node, config, port)
	if err != nil {
		return db.Inbound{}, err
	}
	base := info.Name
	if info.ID == presets.Custom {
		base = t.Type()
	}
	var row db.Inbound
	err = s.st.Tx(ctx, func(q *db.Queries) error {
		if err := CheckPort(ctx, q, node, port, t.Network(), PortHolder{}); err != nil {
			return err
		}
		existing, err := q.ListNodeInbounds(ctx, node.ID)
		if err != nil {
			return err
		}
		now := s.now().Unix()
		row, err = q.CreateInbound(ctx, db.CreateInboundParams{NodeID: node.ID, Name: FreeName(existing, base), Preset: info.ID, Port: port,
			Config: config, CreatedAt: now, UpdatedAt: now})
		return err
	})
	return row, err
}

// CopyInbound is an inbound to add from another one: its template — the same keys,
// passwords and camouflage — on the same or another node, with a port and a REALITY
// target of its own.
type CopyInbound struct {
	SourceID int64
	NodeID   int64  // 0: the source's node
	Port     string // "": the source's
	Dest     string // a REALITY template's new target; "": as the source's
	SNI      string // the name clients send when Dest is an IP; "" takes the host of Dest
}

// Copy adds an inbound that is another one: the same template with its keys at the same
// preset, already on the port it is given. It comes on, with the same listen address,
// automatic switches and traffic pool as its source; its way out is not inherited — an
// exit's relay belongs to the node it was made on, so a copy dials out directly until it
// is given an exit of its own — and its name in subscriptions is the preset's until the
// admin names it.
func (s *Inbounds) Copy(ctx context.Context, c CopyInbound) (db.Inbound, error) {
	src, err := s.st.Q.GetInbound(ctx, c.SourceID)
	if errors.Is(err, sql.ErrNoRows) {
		return db.Inbound{}, ErrUnknownInbound
	}
	if err != nil {
		return db.Inbound{}, err
	}
	node, err := s.node(ctx, c.NodeID)
	if err != nil {
		return db.Inbound{}, err
	}
	port := c.Port
	if port == "" {
		port = src.Port
	}
	if !ValidPort(port) {
		return db.Inbound{}, ErrBadPort
	}
	config, err := applyCopyDest(src.Config, c.Dest, c.SNI)
	if err != nil {
		return db.Inbound{}, err
	}
	t, err := s.CheckTemplate(ctx, node, config, port)
	if err != nil {
		return db.Inbound{}, err
	}
	var row db.Inbound
	err = s.st.Tx(ctx, func(q *db.Queries) error {
		if err := CheckPort(ctx, q, node, port, t.Network(), PortHolder{}); err != nil {
			return err
		}
		existing, err := q.ListNodeInbounds(ctx, node.ID)
		if err != nil {
			return err
		}
		now := s.now().Unix()
		row, err = q.CreateInbound(ctx, db.CreateInboundParams{NodeID: node.ID, Name: FreeName(existing, baseName(src.Name)), Preset: src.Preset, Port: port,
			Config: config, CreatedAt: now, UpdatedAt: now})
		if err != nil {
			return err
		}
		if src.PoolID.Valid {
			if err := q.SetInboundPool(ctx, db.SetInboundPoolParams{PoolID: src.PoolID, ID: row.ID}); err != nil {
				return err
			}
		}
		if src.Listen != "" {
			if err := q.SetInboundListen(ctx, db.SetInboundListenParams{Listen: src.Listen, ID: row.ID}); err != nil {
				return err
			}
		}
		// The insert's defaults are the automatic switches on; the source's flags win.
		if row.AutoPort != src.AutoPort || row.AutoSni != src.AutoSni {
			if err := q.SetInboundAuto(ctx, db.SetInboundAutoParams{AutoPort: src.AutoPort, AutoSni: src.AutoSni, ID: row.ID}); err != nil {
				return err
			}
		}
		row, err = q.GetInbound(ctx, row.ID)
		return err
	})
	return row, err
}

// applyCopyDest points the copy's REALITY camouflage at another site; dest "" keeps the
// template as it is.
func applyCopyDest(config, dest, sni string) (string, error) {
	if dest == "" {
		return config, nil
	}
	p := InboundPatch{Dest: &dest}
	if name := strings.TrimSpace(sni); name != "" {
		p.ServerName = &name
	}
	return editTemplate(config, p)
}

// baseName strips the "-2", "-3"… FreeName adds: a copy of vless-xhttp-2 is a vless-xhttp
// again.
func baseName(name string) string {
	if i := strings.LastIndexByte(name, '-'); i > 0 {
		if _, err := strconv.Atoi(name[i+1:]); err == nil {
			return name[:i]
		}
	}
	return name
}

// Update changes an inbound and returns it before and after. Every field is checked
// before anything is written, and everything is written in one transaction. The checks
// reach the node over the network, so the patch is applied to the row they ran on and the
// transaction writes only if the row is still that one: a change made meanwhile (the
// automatic moves, the CLI, another admin) is checked again, never overwritten with a
// copy of the old row.
func (s *Inbounds) Update(ctx context.Context, id int64, p InboundPatch) (prev, next db.Inbound, err error) {
	for range updateTries {
		prev, next, err = s.update(ctx, id, p)
		if !errors.Is(err, errStale) {
			return prev, next, err
		}
	}
	return db.Inbound{}, db.Inbound{}, ErrInboundChanged
}

func (s *Inbounds) update(ctx context.Context, id int64, p InboundPatch) (prev, next db.Inbound, err error) {
	fail := func(err error) (db.Inbound, db.Inbound, error) { return db.Inbound{}, db.Inbound{}, err }
	prev, err = s.st.Q.GetInbound(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return fail(ErrUnknownInbound)
	}
	if err != nil {
		return fail(err)
	}
	if p.FromPort != nil && prev.Port != *p.FromPort {
		return fail(ErrInboundChanged)
	}
	next = prev
	if p.Listen != nil {
		if next.Listen, err = ParseListen(*p.Listen); err != nil {
			return fail(err)
		}
	}
	if p.AutoPort != nil {
		next.AutoPort = Flag(*p.AutoPort)
	}
	if p.AutoSNI != nil {
		next.AutoSni = Flag(*p.AutoSNI)
	}
	if ListenPinsPort(next.Listen) {
		if p.AutoPort != nil && *p.AutoPort {
			return fail(ErrAutoPortListen)
		}
		if p.AutoSNI != nil && *p.AutoSNI {
			return fail(ErrAutoSNIListen)
		}
		next.AutoPort, next.AutoSni = 0, 0
	}
	if p.PoolID != nil {
		next.PoolID = sql.NullInt64{Int64: *p.PoolID, Valid: *p.PoolID != 0}
	}
	if p.Outbound != nil {
		next.Outbound, next.ExitNodeID = *p.Outbound, sql.NullInt64{}
		if *p.Outbound == "node" {
			if p.ExitNodeID == nil {
				return fail(ErrNotFound)
			}
			next.Outbound, next.ExitNodeID = "direct", sql.NullInt64{Int64: *p.ExitNodeID, Valid: true}
		}
	}
	if p.Port != nil {
		if !ValidPort(*p.Port) {
			return fail(ErrBadPort)
		}
		next.Port = *p.Port
	}
	if p.Enabled != nil {
		next.Enabled = Flag(*p.Enabled)
	}
	if p.Config != nil {
		next.Config = *p.Config
	}
	if next.Config, err = editTemplate(next.Config, p); err != nil {
		return fail(err)
	}
	if p.DisplayName != nil {
		next.DisplayName = strings.TrimSpace(*p.DisplayName)
	}
	template, client := p.EditsTemplate(), p.ForClients()
	// The port is checked where the inbound is going to listen: after a move, a new
	// template (its network may change) or when it comes back on.
	listens := next.Enabled != 0 && (template || next.Port != prev.Port || prev.Enabled == 0)
	node, err := s.node(ctx, prev.NodeID)
	if err != nil {
		return fail(err)
	}
	network := InboundNetwork(next)
	switch {
	case template:
		t, err := s.CheckTemplate(ctx, node, next.Config, next.Port)
		if err != nil {
			return fail(err)
		}
		network = t.Network()
	case listens && s.dry != nil:
		// The template stays; the node still tries it on the new port.
		if err := s.tryOnNode(ctx, node, next.Config, next.Port); err != nil {
			return fail(err)
		}
	}
	err = s.st.Tx(ctx, func(q *db.Queries) error {
		cur, err := q.GetInbound(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUnknownInbound
		}
		if err != nil {
			return err
		}
		if cur != prev {
			return errStale
		}
		// What other rows hold is checked on the transaction that writes.
		if listens {
			if err := CheckPort(ctx, q, node, next.Port, network, InboundHolder(prev)); err != nil {
				return err
			}
		}
		if p.DisplayName != nil {
			// Names are per node: other nodes' links get their own flag prefix.
			siblings, err := q.ListNodeInbounds(ctx, prev.NodeID)
			if err != nil {
				return err
			}
			for _, e := range siblings {
				if e.ID != prev.ID && strings.EqualFold(ProxyName(e), ProxyName(next)) {
					return &NameInUseError{Owner: e.Name}
				}
			}
		}
		if p.PoolID != nil && next.PoolID.Valid {
			if _, err := q.GetTrafficPool(ctx, next.PoolID.Int64); errors.Is(err, sql.ErrNoRows) {
				return ErrUnknownPool
			} else if err != nil {
				return err
			}
		}
		// The exit's chain is checked, and its relay made, before the inbound changes.
		if p.Outbound != nil && next.ExitNodeID.Valid {
			if err := UseExit(ctx, q, prev.NodeID, next.ExitNodeID.Int64, s.now(), s.host); err != nil {
				return err
			}
		}
		if p.PoolID != nil {
			if err := q.SetInboundPool(ctx, db.SetInboundPoolParams{PoolID: next.PoolID, ID: id}); err != nil {
				return err
			}
		}
		if p.Outbound != nil {
			if err := q.SetInboundExit(ctx, db.SetInboundExitParams{ExitNodeID: next.ExitNodeID, Outbound: next.Outbound, ID: id}); err != nil {
				return err
			}
		}
		if next.Listen != prev.Listen {
			if err := q.SetInboundListen(ctx, db.SetInboundListenParams{Listen: next.Listen, ID: id}); err != nil {
				return err
			}
		}
		if next.AutoPort != prev.AutoPort || next.AutoSni != prev.AutoSni {
			if err := q.SetInboundAuto(ctx, db.SetInboundAutoParams{AutoPort: next.AutoPort, AutoSni: next.AutoSni, ID: id}); err != nil {
				return err
			}
		}
		if client {
			if _, err := q.UpdateInbound(ctx, db.UpdateInboundParams{Port: next.Port, Enabled: next.Enabled, Config: next.Config, DisplayName: next.DisplayName,
				UpdatedAt: s.now().Unix(), ID: id}); err != nil {
				return err
			}
		}
		next, err = q.GetInbound(ctx, id)
		return err
	})
	if err != nil {
		return fail(err)
	}
	return prev, next, nil
}

// templateEdit is a form field applied to a template.
type templateEdit struct {
	field string
	apply func(proto.Template) error
}

// editTemplate applies the patch's form fields to a template; a field the template does
// not take is an *EditError.
func editTemplate(config string, p InboundPatch) (string, error) {
	var edits []templateEdit
	if p.Dest != nil {
		sni := ""
		if p.ServerName != nil {
			sni = strings.TrimSpace(*p.ServerName)
		}
		edits = append(edits, templateEdit{"dest", func(t proto.Template) error { return presets.SetDest(t, strings.TrimSpace(*p.Dest), sni) }})
	}
	if p.Fingerprint != nil {
		edits = append(edits, templateEdit{"fingerprint", func(t proto.Template) error {
			if !proto.UsesFingerprint(t) {
				return &proto.Error{Code: "fingerprint_no_tls", Field: "mikan.client.fingerprint"}
			}
			return proto.SetFingerprint(t, strings.TrimSpace(*p.Fingerprint))
		}})
	}
	if p.Obfs != nil {
		edits = append(edits, templateEdit{"obfs", func(t proto.Template) error { return proto.SetObfs(t, *p.Obfs, secure.Token(24)) }})
	}
	if p.XHTTP != nil {
		edits = append(edits, templateEdit{"xhttp", func(t proto.Template) error { return proto.SetXHTTP(t, *p.XHTTP) }})
	}
	if c := p.Client; c != nil {
		edits = append(edits, templateEdit{"client", func(t proto.Template) error {
			return proto.SetClientEndpoint(t, strings.TrimSpace(c.Server), c.Port, strings.TrimSpace(c.SNI))
		}})
	}
	for _, e := range edits {
		t, err := proto.Parse(config)
		if err == nil {
			err = e.apply(t)
		}
		var pe *proto.Error
		if errors.As(err, &pe) {
			return "", &EditError{Field: e.field, Err: pe}
		}
		if err != nil {
			return "", err
		}
		config = proto.Marshal(t)
	}
	return config, nil
}

// CheckTemplate parses and checks a template for an inbound of node on port: mikan's
// rules first, then mihomo's own parser on the node, so a broken template never replaces
// a working listener. A node that cannot be reached does not stop it: the node checks
// again when it applies the template.
func (s *Inbounds) CheckTemplate(ctx context.Context, node db.Node, config, port string) (proto.Template, error) {
	t, err := proto.Parse(config)
	if err != nil {
		return nil, err
	}
	selfSteal, err := s.selfStealPort(ctx, node)
	if err != nil {
		return nil, err
	}
	if err := proto.Validate(t, proto.Options{SelfStealPort: selfSteal}); err != nil {
		return nil, err
	}
	// Checked here, not in proto.Validate: nodes keep applying templates saved before.
	if fp := t.Ext().Client.Fingerprint; fp != "" && !proto.ValidFingerprint(fp) {
		return nil, &proto.Error{Code: "config_fingerprint", Field: "mikan.client.fingerprint", Detail: fp}
	}
	// mihomo's ws client ignores reality-opts and Xray has no REALITY over WebSocket: the
	// client would do plain TLS to the masking site.
	if t["ws-path"] != nil && t["reality-config"] != nil {
		return nil, &proto.Error{Code: "config_ws_reality", Field: "ws-path"}
	}
	if err := s.checkDestResolves(ctx, t); err != nil {
		return nil, err
	}
	if err := s.dryRun(ctx, node, t, port, selfSteal); err != nil {
		return nil, err
	}
	return t, nil
}

// SystemResolve looks a name up with the system's resolver: its IPv4 addresses.
func SystemResolve(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
}

// SetResolver makes a REALITY target given by name be looked up when it is saved; without
// one (tests) names are taken as they are.
func (s *Inbounds) SetResolver(resolve func(ctx context.Context, host string) ([]netip.Addr, error)) {
	s.resolve = resolve
}

// SetHostLookup lets the relay an inbound's new exit gets keep off ports other programs
// hold on the exit's server; without one (tests, the CLI) the relay takes what the panel
// knows is free.
func (s *Inbounds) SetHostLookup(host HostLookup) {
	s.host = host
}

// checkDestResolves refuses a REALITY target whose name leads to this host or its
// network: proto.Validate reads the text, and every DNS name looks public. The node dials
// the target for every probe of the port, past the rules that fence its users in, so such
// a name would publish an internal service to the internet. A name that cannot be looked
// up is let through: the node looks it up where it dials.
func (s *Inbounds) checkDestResolves(ctx context.Context, t proto.Template) error {
	if s.resolve == nil {
		return nil
	}
	dest, _ := presets.Dest(t)
	host, _, err := net.SplitHostPort(dest)
	if err != nil || host == "" {
		return nil
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return nil // a literal address: proto.Validate has judged it
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	addrs, err := s.resolve(ctx, host)
	if err != nil {
		return nil
	}
	for _, a := range addrs {
		if !proto.PublicAddr(a) {
			return &proto.Error{Code: "reality_dest_private", Field: "reality-config.dest", Detail: host}
		}
	}
	return nil
}

// tryOnNode runs a saved template through the node's mihomo on another port.
func (s *Inbounds) tryOnNode(ctx context.Context, node db.Node, config, port string) error {
	t, err := proto.Parse(config)
	if err != nil {
		return err
	}
	selfSteal, err := s.selfStealPort(ctx, node)
	if err != nil {
		return err
	}
	return s.dryRun(ctx, node, t, port, selfSteal)
}

func (s *Inbounds) dryRun(ctx context.Context, node db.Node, t proto.Template, port string, selfSteal int) error {
	if s.dry == nil {
		return nil
	}
	err := s.dry.Validate(ctx, node.ID, nodeapi.ValidateRequest{Inbound: nodeapi.Inbound{Name: "validate", Port: port, Config: t.JSON()}, SelfStealPort: selfSteal})
	if errors.Is(err, nodeapi.ErrUnavailable) {
		return nil
	}
	return err
}

// selfStealPort is the panel's port for the panel's own node, the only one that may use
// the panel's HTTPS as its REALITY target; 0 for the others.
func (s *Inbounds) selfStealPort(ctx context.Context, node db.Node) (int, error) {
	if node.Address != "" {
		return 0, nil
	}
	p, _, err := settings.Get[int](ctx, settings.New(s.st.Q), settings.KeyPanelPort)
	return p, err
}

// Find is a node's inbound by name, the way the CLI names inbounds.
func (s *Inbounds) Find(ctx context.Context, nodeID int64, name string) (db.Inbound, error) {
	if _, err := s.node(ctx, nodeID); err != nil {
		return db.Inbound{}, err
	}
	existing, err := s.st.Q.ListNodeInbounds(ctx, nodeID)
	if err != nil {
		return db.Inbound{}, err
	}
	i := slices.IndexFunc(existing, func(e db.Inbound) bool { return e.Name == name })
	if i < 0 {
		return db.Inbound{}, ErrUnknownInbound
	}
	return existing[i], nil
}

// node loads the node an inbound belongs to; 0 is the panel's own node.
func (s *Inbounds) node(ctx context.Context, id int64) (db.Node, error) {
	if id == 0 {
		id = 1
	}
	n, err := s.st.Q.GetNode(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return n, ErrUnknownNode
	}
	return n, err
}

// ValidPort accepts a port ("443") or a Hysteria2 hopping range ("20000-30000").
func ValidPort(spec string) bool {
	_, _, ok := parsePort(spec)
	return ok
}

// ParseListen reads the address an inbound listens on: "" (or 0.0.0.0, ::) for every
// address, else one IPv4 or IPv6 address, e.g. 127.0.0.1 behind nginx on the same server.
func ParseListen(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil || a.Zone() != "" || a.IsMulticast() {
		return "", ErrBadListen
	}
	if a.IsUnspecified() {
		return "", nil
	}
	return a.Unmap().String(), nil
}

// ListenPinsPort: an inbound on an address of its own sits behind a TCP proxy (nginx
// stream, HAProxy) that forwards to its port, so the port must not move on its own. Two
// inbounds of a node still may not share a port number on different addresses: one rule
// for every check, and a bind on every address takes the port on all of them.
func ListenPinsPort(listen string) bool { return listen != "" }

// ListenPinsSNI: the same proxy may pick the backend by the site's name (nginx stream,
// HAProxy), and the panel cannot tell, so a new site would lose the inbound's clients.
func ListenPinsSNI(listen string) bool { return listen != "" }

// InboundNetwork is the network the inbound's port is bound on: "tcp" or "udp".
func InboundNetwork(in db.Inbound) string {
	if t, err := proto.Parse(in.Config); err == nil {
		return t.Network()
	}
	info, _ := presets.Get(in.Preset)
	return info.Network
}

// ProxyName is the name an inbound gets in subscriptions.
func ProxyName(in db.Inbound) string {
	if in.DisplayName != "" {
		return in.DisplayName
	}
	info, _ := presets.Get(in.Preset)
	return info.SubName
}

// FreeName returns base, or base-2, base-3… when an inbound already has that name.
func FreeName(existing []db.Inbound, base string) string {
	taken := map[string]bool{}
	for _, e := range existing {
		taken[e.Name] = true
	}
	name := base
	for i := 2; taken[name]; i++ {
		name = base + "-" + strconv.Itoa(i)
	}
	return name
}
