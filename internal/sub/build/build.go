package build

import (
	"crypto/sha256"
	"encoding/binary"
	"strconv"
	"strings"
	"unicode"

	"mikan/internal/proto"
	"mikan/internal/sub/apps"
	"mikan/internal/sub/model"
	"mikan/internal/textutil"
)

// Profile is what the proxies of one user are made of.
type Profile struct {
	Keys     proto.Slot
	Inbounds []model.Inbound // the ones the user may use, in display order
	// Nodes are the enabled nodes in display order; the inbounds of other nodes are left out.
	Nodes []model.Node
	// Fingerprint is the panel's default uTLS profile (Input.Fingerprint).
	Fingerprint string
	// Vars are the user's values of the variables a host's name may have (serve.Values);
	// nil leaves them as written.
	Vars map[string]string
	// Seed picks the user's one of a host's more addresses and TLS names: the subscription's
	// token, so that a user keeps them and users spread over them. "": the host's own.
	Seed string
	// Groups are the profile's groups, with their defaults: a proxy never takes the name of
	// one, nor of PROXY. The groups of countries and routes give way to the proxies.
	Groups model.GroupNames
	// Skip, when set, hears of an inbound that is left out because it cannot be rendered
	// (a port that is no number, a template that no longer parses): one broken inbound must
	// not empty or fail the subscription of everyone.
	Skip func(in model.Inbound, err error)
}

// group says whether name is one of p's groups, in any case, as the apps compare them.
func (p Profile) group(name string) bool {
	for _, g := range []string{p.Groups.Main, p.Groups.Auto, model.AliasGroup} {
		if g != "" && strings.EqualFold(name, g) {
			return true
		}
	}
	return false
}

func (p Profile) skip(in model.Inbound, err error) {
	if p.Skip != nil {
		p.Skip(in, err)
	}
}

// Proxies are the user's proxies in the panel's order of its servers (servers): the places
// the admin gave the hosts, and without them node by node in the nodes' order, in a node the
// inbounds' own, in an inbound its hosts'.
func Proxies(p Profile) []model.Proxy {
	list := p.servers()
	out := make([]model.Proxy, 0, len(list))
	used := map[string]bool{}
	for _, s := range list {
		// A host's own name is the admin's, as typed: no flag of the node.
		own := s.base
		if s.h.name != "" {
			if v := fillName(s.h.name, p.Vars); v != "" {
				own = v
			}
		}
		// mihomo refuses a profile with two proxies of the same name, or a proxy named as a
		// group: the second one of a name is "Name 2" (a second host of the inbound that has
		// no name of its own), a user's {name} that is the main group's "VPN 2".
		name := own
		for i := 2; used[name] || p.group(name); i++ {
			name = own + " " + strconv.Itoa(i)
		}
		c, err := Proxy(s.tpl.take(), Input{Name: name, Host: s.n.Endpoint.Host, Port: s.port, PortSpec: s.spec,
			SNI: s.n.Endpoint.SNI, PinSHA256: s.n.Endpoint.PinSHA256, Fingerprint: p.Fingerprint, Keys: p.Keys, SSOneKey: s.n.SSOneKey,
			Client: s.h.client, OwnClient: s.h.own, Options: s.h.options})
		if err != nil {
			p.skip(s.in, err)
			continue
		}
		used[name] = true
		c.Node, c.Pool, c.NoAuto, c.Host = s.n.ID, s.in.Pool, s.in.NoAuto, s.h.id
		out = append(out, c)
	}
	return out
}

// host is one place an inbound is reached at, as Proxy takes it.
type host struct {
	id      int64 // the panel's (model.Host.ID); 0: the template's, of an inbound without hosts
	order   int64 // its place among the panel's servers (model.Host.Order)
	client  proto.ClientOverrides
	own     bool // client is the whole of it, not over the template's mikan.client
	options Options
	name    string // its own, the variables not yet filled
	hidden  bool   // the default host, which the user is not shown
}

// hosts are the places clients reach in at, one proxy each. The default host is the
// template's mikan.client, which it repeats: that one reads the template, so an inbound
// gives what it gave before it had hosts, with the default host's own fields. The others
// are whole, an empty field is what clients get without it. An inbound with no hosts is
// reached at the default alone. A hidden host (model.Host.Hidden) is left out, the default
// one too: an inbound whose hosts are all hidden gives the user nothing.
func hosts(in model.Inbound, seed string, sniOK bool) []host {
	out := []host{{}}
	for i, h := range in.Hosts {
		if h.Hidden {
			if i == 0 {
				out[0].hidden = true
			}
			continue
		}
		o := Options{ALPN: h.ALPN, IPVersion: h.IPVersion, Mux: h.Mux, MuxProtocol: h.MuxProtocol, MuxPadding: h.MuxPadding, CDN: h.CDN}
		addr := pick(seed, "address", h.ID, h.Address, h.Addresses)
		sni := h.SNI
		if sniOK {
			// A template that no longer takes a TLS name of clients' (REALITY's) gets none.
			sni = pick(seed, "sni", h.ID, h.SNI, h.SNIs)
		}
		if i == 0 {
			// The default host is the template's; a choice of its more values goes over it.
			out[0].options, out[0].name, out[0].id, out[0].order = o, h.Name, h.ID, h.Order
			if addr != h.Address {
				out[0].client.Server = addr // one of the more ones: never empty
			}
			if sni != h.SNI {
				out[0].client.SNI = sni
			}
			continue
		}
		out = append(out, host{id: h.ID, order: h.Order, own: true, options: o, name: h.Name, client: proto.ClientOverrides{Server: addr, Port: h.Port,
			SNI: sni, Host: h.Host, Fingerprint: h.Fingerprint}})
	}
	// The user's group reaches the default host elsewhere: what it sets goes over the rest,
	// a choice of the more values too.
	if o := in.Override; o != (model.AddressOverride{}) {
		if o.Address != "" {
			out[0].client.Server = o.Address
		}
		if o.Port != 0 {
			out[0].client.Port = o.Port
		}
		if o.SNI != "" && sniOK {
			out[0].client.SNI = o.SNI
		}
		if o.Host != "" {
			out[0].client.Host = o.Host
		}
		if o.Name != "" {
			out[0].name = o.Name
		}
		out[0].options.CDN = out[0].options.CDN || o.CDN
	}
	if out[0].hidden {
		return out[1:]
	}
	return out
}

// pick is the user's one of a host's own value and its more ones: the same for one seed (the
// user's token), spread over the users; the host's own without a seed or more values. kind
// and the host's id, which no change of the other hosts moves, make each list's pick its own.
func pick(seed, kind string, host int64, own string, more []string) string {
	if seed == "" || len(more) == 0 {
		return own
	}
	// SHA-256, whose every bit is mixed: FNV's lowest one is only the parity of the bytes,
	// so with two values each the address and the TLS name would always go together.
	sum := sha256.Sum256([]byte(seed + "|" + kind + "|" + strconv.FormatInt(host, 10)))
	if i := binary.BigEndian.Uint64(sum[:8]) % uint64(len(more)+1); i > 0 {
		return more[i-1]
	}
	return own
}

// HostNames are the names of the hosts of ins that have variables to fill ({…}).
func HostNames(ins []model.Inbound) []string {
	var out []string
	for _, in := range ins {
		for _, h := range in.Hosts {
			if !h.Hidden && strings.Contains(h.Name, "{") {
				out = append(out, h.Name)
			}
		}
		if strings.Contains(in.Override.Name, "{") {
			out = append(out, in.Override.Name)
		}
	}
	return out
}

// NameMax bounds a host's name filled for a user: the admin's is at most 48 letters
// (model.ValidName), the values may make it longer.
const NameMax = 64

// fillName is a host's name with the user's values in it: one line without the marks a name
// may not have (model.ValidName), at most NameMax letters. "" when it is no name to give a
// proxy, blank or one of mihomo's own policies: the inbound's is used.
func fillName(name string, vars map[string]string) string {
	s := shownName(strings.Map(func(r rune) rune {
		if strings.ContainsRune(model.NameBadChars, r) {
			return ' '
		}
		return r
	}, textutil.Fill(name, vars, nil)))
	if rs := []rune(s); len(rs) > NameMax {
		s = strings.TrimSpace(string(rs[:NameMax]))
	}
	if s == "" || model.Reserved(s) {
		return ""
	}
	return s
}

// NodePrefix is what precedes proxy names of a node when a subscription has several:
// the flag a name starts with, or the whole name.
func NodePrefix(name string) string {
	rs := []rune(strings.TrimSpace(name))
	if len(rs) >= 2 && isRegional(rs[0]) && isRegional(rs[1]) {
		return string(rs[:2])
	}
	return string(rs)
}

// shownName is a proxy name without control characters and the spaces around it. The
// panel never saves them, but a name written to the database past it must not fail the
// profile: the YAML encoder cannot write a name that starts with a line break. Only proxy
// names are cleaned here, not the group names.
func shownName(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s))
}

func isRegional(r rune) bool { return r >= 0x1F1E6 && r <= 0x1F1FF }

// ForApp keeps the inbounds the app can use. Inbounds with one key for everyone go only
// to users whose access is on (active): the node cannot cut anyone off there. One whose
// template no longer parses is kept: Proxies leaves it out and tells Profile.Skip, so the
// admin hears of it.
func ForApp(ins []model.Inbound, app apps.App, active bool) []model.Inbound {
	out := make([]model.Inbound, 0, len(ins))
	for _, in := range ins {
		t, err := parseTemplate(in.Config)
		if err == nil && (!app.Supports(proto.NeedsOf(t)) || proto.Shared(t) && !active) {
			continue
		}
		out = append(out, in)
	}
	return out
}
