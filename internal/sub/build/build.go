package build

import (
	"fmt"
	"strconv"
	"strings"

	"mikan/internal/proto"
	"mikan/internal/sub/apps"
	"mikan/internal/sub/model"
)

// Profile is what the proxies of one user are made of.
type Profile struct {
	Keys     proto.Slot
	Inbounds []model.Inbound // the ones the user may use, in display order
	// Nodes are the enabled nodes in display order; the inbounds of other nodes are left out.
	Nodes []model.Node
	// Fingerprint is the panel's default uTLS profile (Input.Fingerprint).
	Fingerprint string
	// Skip, when set, hears of an inbound that is left out because it cannot be rendered
	// (a port that is no number, a template that no longer parses): one broken inbound must
	// not empty or fail the subscription of everyone.
	Skip func(in model.Inbound, err error)
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
			base := in.Name
			// A name the admin typed is used as is; preset names get the node's flag.
			if prefix := NodePrefix(n.Name); multi && !in.Named && prefix != "" {
				base = prefix + " " + base
			}
			// mihomo refuses a profile with two proxies of the same name.
			name := base
			for i := 2; used[name]; i++ {
				name = base + " " + strconv.Itoa(i)
			}
			c, err := Proxy(t, Input{Name: name, Host: n.Endpoint.Host, Port: port, PortSpec: spec,
				SNI: n.Endpoint.SNI, PinSHA256: n.Endpoint.PinSHA256, Fingerprint: p.Fingerprint, Keys: p.Keys, SSOneKey: n.SSOneKey})
			if err != nil {
				p.skip(in, err)
				continue
			}
			used[name] = true
			c.Node, c.Pool, c.NoAuto = n.ID, in.Pool, in.NoAuto
			out = append(out, c)
		}
	}
	return out
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
