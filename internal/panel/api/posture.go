package api

import (
	"net"
	"slices"
	"strconv"
	"strings"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/presets"
	"mikan/internal/panel/store/db"
	"mikan/internal/proto"
	"mikan/internal/scan"
)

// The security presets of Settings → Security: two ways the whole installation is set up,
// each a list of changes the admin reads before anything is written.
//
// "standard" is how the panel sets itself up: every protocol on a port of its own. "secure"
// is the least a server can show: one TCP port 443 a node, its protocols behind it told
// apart by the name a client asks for, every REALITY protocol with a name of its own or, on a
// node whose router tells them apart by key (router_by_key, which the preset turns on), with
// one of the panel's names it shares, the node's API closed to anyone but the panel and a
// front page everywhere. The names are the
// panel's own (its domain, the subscriptions' domain): the server then shows its own
// certificate and nobody else's. A stranger's site next to the server is taken only when the
// admin asks for it in the plan (neighbors). What a preset cannot do for the admin (no name
// left, a port another program holds) is a line of its own, never a silent skip.

const (
	postureStandard = "standard"
	postureSecure   = "secure"
	postureCustom   = "custom"
)

// PostureChange is one change of a preset.
type PostureChange struct {
	Node      int64             `json:"node" doc:"Нода; 0: сама панель"`
	NodeName  string            `json:"node_name,omitempty"`
	InboundID int64             `json:"inbound_id,omitempty"`
	Inbound   string            `json:"inbound,omitempty"`
	Code      string            `json:"code" enum:"panel_page,front_page,site,port,through_443,direct,router_on,router_by_key,router_off,api_lockdown,auto_off,disable,port_443_busy,port_443_panel,same_name,no_site,stranger_site,no_port,udp_ports,ssh_open" doc:"Что меняется; у строки manual: что остаётся сделать самому"`
	Params    map[string]string `json:"params,omitempty"`
	Manual    bool              `json:"manual" doc:"Пресет этого сделать не может: строка для админа"`
	Optional  bool              `json:"optional,omitempty" doc:"Изменение по желанию: применяется, только если админ его отметил"`
	Error     string            `json:"error,omitempty" doc:"После применения: почему не получилось"`
}

// postureNode is a node as a preset reads it.
type postureNode struct {
	node     db.Node
	inbounds []db.Inbound
	spare    []string      // free TCP ports for an inbound that leaves 443 to the router
	sites    []scan.Result // the sites next to the server, the best first
	scanned  bool          // sites were looked for: none left means none
	// byKey: the node's router can tell REALITY protocols that share a name apart by key,
	// so they may share the panel's names (domain.SNIClashes).
	byKey bool
}

// posture is the installation as a preset reads it.
type posture struct {
	nodes     []postureNode
	own       []string // the panel's names that serve as a REALITY site on its own node
	panelPort int
	page      string // the panel's front page, "" none
	pagePick  string // the page for where there is none
	// neighbors: a protocol the panel's own names do not cover may take a stranger's site
	// next to the server. Off, it stays as it is and the admin is told.
	neighbors bool
}

// standard is what turns the installation back to the panel's own setup.
func (p posture) standard() []PostureChange {
	var out []PostureChange
	for _, pn := range p.nodes {
		n := pn.node
		if n.Enabled == 0 {
			continue
		}
		for _, in := range pn.inbounds {
			t, err := proto.Parse(in.Config)
			if err != nil || in.Enabled == 0 || !proto.SNI443(t) {
				continue
			}
			out = append(out, PostureChange{Node: n.ID, NodeName: n.Name, InboundID: in.ID, Inbound: in.Name, Code: "direct", Params: map[string]string{"port": in.Port}})
		}
		if n.SniRouter != 0 {
			out = append(out, PostureChange{Node: n.ID, NodeName: n.Name, Code: "router_off"})
		}
	}
	return out
}

// secure is what makes the installation show the least.
func (p posture) secure() []PostureChange {
	var out []PostureChange
	page := p.page
	if page == "" {
		page = p.pagePick
		out = append(out, PostureChange{Code: "panel_page", Params: map[string]string{"page": page}})
	}
	for _, pn := range p.nodes {
		n := pn.node
		if n.Enabled == 0 {
			continue
		}
		add := func(c PostureChange) {
			c.Node, c.NodeName = n.ID, n.Name
			out = append(out, c)
		}
		local := n.Address == ""
		type entry struct {
			in db.Inbound
			t  proto.Template
		}
		var routable []entry
		holder := "" // an inbound the router cannot take holds TCP 443
		for _, in := range pn.inbounds {
			t, err := proto.Parse(in.Config)
			if err != nil || in.Enabled == 0 {
				continue
			}
			if proto.CanSNI443(t) && !domain.ListenPinsPort(in.Listen) {
				routable = append(routable, entry{in, t})
			} else if in.Port == "443" && t.Network() != "udp" {
				holder = in.Name
			}
		}
		if local && p.panelPort == 443 && len(routable) > 0 {
			// The panel itself listens on 443 of this server: the node's router has no port.
			add(PostureChange{Code: "port_443_panel", Manual: true})
		} else if holder != "" {
			add(PostureChange{Inbound: holder, Code: "port_443_busy", Manual: true})
		} else if len(routable) > 0 {
			var own []string
			if local && p.panelPort > 0 {
				own = p.own
			}
			// The names that stay: an inbound keeps its site while no other has it and it
			// is a site worth showing. One that keeps a stranger's site is noted apart:
			// it moves to a name of the panel's own while there is one free.
			const (
				needs    = iota // no name it can keep
				keeps           // its name stays
				stranger        // its name stays, and is somebody else's site
			)
			// With the router telling REALITY protocols apart by key (router_by_key) they
			// share names: a REALITY one is never in another's way, and one that is no
			// REALITY one only of another such.
			share := pn.byKey
			taken := map[string]bool{} // the names protocols keep or get
			plain := map[string]bool{} // of them, the names a protocol that is no REALITY one holds
			inWay := func(name string, reality bool) bool {
				switch {
				case !share:
					return taken[name]
				case reality:
					return false
				}
				return plain[name]
			}
			hold := func(name string, reality bool) {
				taken[name] = true
				if !reality {
					plain[name] = true
				}
			}
			state := make([]int, len(routable))
			for i, e := range routable {
				names := lowerNames(proto.SNINames(e.t, n.Domain))
				reality := proto.Reality(e.t)
				ok, foreign := true, false
				if reality {
					dest, _ := presets.Dest(e.t)
					ok = len(names) > 0 && dest != presets.DefaultDest
				}
				for _, name := range names {
					ok = ok && !inWay(name, reality) && !(reality && scan.Weak(name))
					foreign = foreign || !slices.Contains(own, name)
				}
				if !ok {
					continue
				}
				for _, name := range names {
					hold(name, reality)
				}
				if state[i] = keeps; reality && foreign {
					state[i] = stranger
				}
			}
			// The panel's own names nobody has yet: first to the protocols with no name to
			// keep, then to those on a stranger's site, in the order of the list. When they
			// run out and the protocols share names, the first that no other kind holds.
			picks := map[int]string{}
			for _, want := range []int{needs, stranger} {
				for i, e := range routable {
					if state[i] != want || !proto.Reality(e.t) {
						continue
					}
					pick := ""
					for _, name := range own {
						if !taken[name] {
							pick = name
							break
						}
					}
					for _, name := range own {
						if pick == "" && share && !plain[name] {
							pick = name
						}
					}
					if pick != "" {
						picks[i] = pick
						hold(pick, true)
					}
				}
			}
			spare := pn.spare
			behind, blocked := 0, false
			for i, e := range routable {
				c := PostureChange{InboundID: e.in.ID, Inbound: e.in.Name}
				// stays: the inbound is left as it is, with a line for the admin; on port
				// 443 it leaves the router no port.
				stays := func(code string, params map[string]string) {
					m := c
					m.Code, m.Params, m.Manual = code, params, true
					add(m)
					blocked = blocked || e.in.Port == "443"
				}
				names := lowerNames(proto.SNINames(e.t, n.Domain))
				if name, ok := picks[i]; ok {
					s := c
					s.Code, s.Params = "site", map[string]string{"from": strings.Join(names, ", "), "dest": net.JoinHostPort("127.0.0.1", strconv.Itoa(p.panelPort)), "sni": name}
					add(s)
				} else if state[i] == stranger && len(own) > 0 {
					// It keeps somebody else's site only because the panel's own names ran
					// out: said, so the admin can add one more.
					m := c
					m.Code, m.Params, m.Manual = "stranger_site", map[string]string{"sni": strings.Join(names, ", ")}, true
					add(m)
				} else if state[i] == needs {
					if !proto.Reality(e.t) {
						stays("same_name", map[string]string{"name": strings.Join(names, ", ")})
						continue
					}
					// No name of the panel's own is left for it. A stranger's site is the
					// admin's call: without it the protocol stays where it is.
					if !p.neighbors {
						stays("no_site", map[string]string{"from": strings.Join(names, ", ")})
						continue
					}
					dest, sni := "", ""
					for _, r := range pn.sites {
						if name := strings.ToLower(r.SNI); dest == "" && r.OK && name != "" && !scan.Weak(name) && !taken[name] {
							dest, sni = r.Dest, name
						}
					}
					if dest == "" && pn.scanned {
						stays("no_site", map[string]string{"from": strings.Join(names, ", ")})
						continue
					}
					s := c
					s.Code, s.Params = "site", map[string]string{"from": strings.Join(names, ", ")}
					if dest != "" {
						s.Params["dest"], s.Params["sni"] = dest, sni
						taken[sni] = true
					}
					add(s)
				}
				if e.in.Port == "443" {
					if len(spare) == 0 {
						stays("no_port", nil)
						continue
					}
					m := c
					m.Code, m.Params = "port", map[string]string{"from": e.in.Port, "to": spare[0]}
					spare = spare[1:]
					add(m)
				}
				if !proto.SNI443(e.t) {
					m := c
					m.Code, m.Params = "through_443", map[string]string{"port": e.in.Port}
					add(m)
				}
				behind++
			}
			if behind > 0 && !blocked && n.SniRouter == 0 {
				add(PostureChange{Code: "router_on"})
			}
			if behind > 0 && !blocked && share && n.SniByKey == 0 {
				add(PostureChange{Code: "router_by_key"})
			}
		}
		// What cannot go behind 443. A protocol with no handshake to read keeps a TCP port
		// that answers nothing: turning it off is offered and is the admin's call, since
		// its clients lose it. The ones over UDP are named: no TCP scan finds them, and
		// nothing puts them behind a TCP port.
		var udp []string
		for _, in := range pn.inbounds {
			t, err := proto.Parse(in.Config)
			if err != nil || in.Enabled == 0 {
				continue
			}
			// The automatic moves are off: a port the panel picks by itself is a new port
			// outside, and a site it picks by itself may be a stranger's again.
			if in.AutoPort != 0 || in.AutoSni != 0 {
				add(PostureChange{InboundID: in.ID, Inbound: in.Name, Code: "auto_off"})
			}
			switch {
			case proto.Silent(t):
				add(PostureChange{InboundID: in.ID, Inbound: in.Name, Code: "disable", Optional: true, Params: map[string]string{"port": in.Port, "type": t.Type()}})
			case t.Network() == "udp":
				udp = append(udp, in.Name)
			}
		}
		if len(udp) > 0 {
			add(PostureChange{Code: "udp_ports", Manual: true, Params: map[string]string{"inbounds": strings.Join(udp, ", ")}})
		}
		if !local && n.ApiLockdown == 0 {
			add(PostureChange{Code: "api_lockdown"})
		}
		if n.FrontPage == "" {
			add(PostureChange{Code: "front_page", Params: map[string]string{"page": page}})
		}
		// SSH among the ports the server answers on by itself is the admin's own way in: the
		// preset names it and leaves it.
		if slices.Contains(n.OwnPorts, 22) {
			add(PostureChange{Code: "ssh_open", Manual: true})
		}
	}
	return out
}

func lowerNames(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if n = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(n), ".")); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// pending counts what a preset would change, the admin's own lines aside.
func pending(changes []PostureChange) int {
	n := 0
	for _, c := range changes {
		if !c.Manual && !c.Optional {
			n++
		}
	}
	return n
}

// current names the preset the installation is on: secure when that preset has nothing
// left to change, standard when nothing is behind a shared 443, custom between the two.
func (p posture) current() (name string, secure, standard int) {
	secure, standard = pending(p.secure()), len(p.standard())
	switch {
	case secure == 0:
		return postureSecure, secure, standard
	case standard == 0:
		return postureStandard, secure, standard
	}
	return postureCustom, secure, standard
}
