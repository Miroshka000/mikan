package build

import (
	"cmp"
	"fmt"
	"slices"

	"mikan/internal/proto"
	"mikan/internal/sub/model"
)

// server is one proxy Proxies makes: a host of an inbound of a node, before its name.
type server struct {
	n    model.Node
	in   model.Inbound
	tpl  *template // the inbound's, shared by its servers
	port int
	spec string // the port as the inbound has it, a Hysteria2 range too
	base string // the name it takes without a host's own: the inbound's, the node's flag in front
	h    host
}

// template is an inbound's parsed template, which every server of the inbound renders.
type template struct {
	t     proto.Template
	taken bool
}

// take is the template for one proxy: the first one gets it, the others a copy of their own,
// since Proxy may share maps with the template it was given.
func (t *template) take() proto.Template {
	if t.taken {
		return t.t.Clone()
	}
	t.taken = true
	return t.t
}

// servers are the proxies of p to make, in the panel's order. They come node by node in the
// nodes' order, in a node the inbounds', in an inbound its hosts', and then go to the places
// of their hosts (placed). An inbound that cannot be rendered is left out and told to Skip.
func (p Profile) servers() []server {
	var out []server
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
			tpl := &template{t: t}
			for _, h := range hosts(in, p.Seed, proto.ClientSNI(t)) {
				out = append(out, server{n: n, in: in, tpl: tpl, port: port, spec: spec, base: base, h: h})
			}
		}
	}
	return placed(out)
}

// placed puts list, in the order the servers come, in the order of their hosts' places
// (model.Host.Order). A tie keeps the order they came in: a host added to an inbound takes the
// place of its last one, a new inbound that of its node's last server. A server without a
// place (0: a profile of no panel, an inbound reached at its template alone) keeps the place
// of the one before it, so a profile without places comes as it is.
func placed(list []server) []server {
	last := int64(0)
	for i := range list {
		if list[i].h.order == 0 {
			list[i].h.order = last
		} else {
			last = list[i].h.order
		}
	}
	slices.SortStableFunc(list, func(a, b server) int { return cmp.Compare(a.h.order, b.h.order) })
	return list
}
