package build

import (
	"fmt"
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

// Proxies are the user's proxies, node by node in the nodes' order, and in a node the
// inbounds' own.
func Proxies(p Profile) []model.Proxy {
	var out []model.Proxy
	used := map[string]bool{}
	multi := len(p.Nodes) > 1
	for _, n := range p.Nodes {
		for _, in := range p.Inbounds {
			if in.Node != n.ID {
				continue
			}
			port := in.Port
			if port == 0 {
				p.skip(in, fmt.Errorf("bad port %q", in.PortSpec))
				continue
			}
			t, err := parseTemplate(in.Config)
			if err != nil {
				// Saved configs are validated; one broken inbound must not empty the subscription.
				p.skip(in, err)
				continue
			}
			spec := in.PortSpec
			if n.Endpoint.SNIRouter && proto.SNI443(t) {
				port, spec = 443, "443"
			}
			base := shownName(in.Name)
			if base == "" {
				base = shownName(in.Label) // nothing was left of it
			}
			// A name the admin typed is used as is; preset names get the node's flag.
			if prefix := NodePrefix(n.Name); multi && !in.Named && prefix != "" {
				base = prefix + " " + base
			}
			for i, h := range hosts(in) {
				// A host's own name is the admin's, as typed: no flag of the node.
				own := base
				if h.name != "" {
					if s := fillName(h.name, p.Vars); s != "" {
						own = s
					}
				}
				// mihomo refuses a profile with two proxies of the same name, or a proxy named
				// as a group: a second host of the inbound is "Name 2", a user's {name} that is
				// the main group's "VPN 2".
				name := own
				for i := 2; used[name] || p.group(name); i++ {
					name = own + " " + strconv.Itoa(i)
				}
				tpl := t
				if i > 0 {
					tpl = t.Clone() // Proxy may share maps with its template
				}
				c, err := Proxy(tpl, Input{Name: name, Host: n.Endpoint.Host, Port: port, PortSpec: spec,
					SNI: n.Endpoint.SNI, PinSHA256: n.Endpoint.PinSHA256, Fingerprint: p.Fingerprint, Keys: p.Keys, SSOneKey: n.SSOneKey,
					Client: h.client, OwnClient: h.own, Options: h.options})
				if err != nil {
					p.skip(in, err)
					continue
				}
				used[name] = true
				c.Node, c.Pool, c.NoAuto = n.ID, in.Pool, in.NoAuto
				out = append(out, c)
			}
		}
	}
	return out
}

// host is one place an inbound is reached at, as Proxy takes it.
type host struct {
	client  proto.ClientOverrides
	own     bool // client is the whole of it, not over the template's mikan.client
	options Options
	name    string // its own, the variables not yet filled
}

// hosts are the places clients reach in at, one proxy each. The default host is the
// template's mikan.client, which it repeats: that one reads the template, so an inbound
// gives what it gave before it had hosts, with the default host's own fields. The others
// are whole, an empty field is what clients get without it. An inbound with no hosts is
// reached at the default alone.
func hosts(in model.Inbound) []host {
	out := []host{{}}
	for i, h := range in.Hosts {
		o := Options{ALPN: h.ALPN, IPVersion: h.IPVersion, Mux: h.Mux, MuxProtocol: h.MuxProtocol, MuxPadding: h.MuxPadding, CDN: h.CDN}
		if i == 0 {
			out[0].options, out[0].name = o, h.Name
			continue
		}
		out = append(out, host{own: true, options: o, name: h.Name, client: proto.ClientOverrides{Server: h.Address, Port: h.Port,
			SNI: h.SNI, Host: h.Host, Fingerprint: h.Fingerprint}})
	}
	return out
}

// HostNames are the names of the hosts of ins that have variables to fill ({…}).
func HostNames(ins []model.Inbound) []string {
	var out []string
	for _, in := range ins {
		for _, h := range in.Hosts {
			if strings.Contains(h.Name, "{") {
				out = append(out, h.Name)
			}
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
