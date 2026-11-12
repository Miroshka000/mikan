package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/frontpage"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/presets"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/targets"
	"mikan/internal/proto"
	"mikan/internal/scan"
)

// The security presets of Settings → Security: two ways the whole installation is set up,
// each a list of changes the admin reads before anything is written.
//
// "standard" is how the panel sets itself up: every protocol on a port of its own. "secure"
// is the least a server can show: one TCP port 443 a node, its protocols behind it told
// apart by the name a client asks for, every REALITY protocol with a name of its own, the
// node's API closed to anyone but the panel and a front page everywhere. The names are the
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
	Code      string            `json:"code" enum:"panel_page,front_page,site,port,through_443,direct,router_on,router_off,api_lockdown,port_443_busy,port_443_panel,same_name,no_site,stranger_site,no_port,ssh_open" doc:"Что меняется; у строки manual: что остаётся сделать самому"`
	Params    map[string]string `json:"params,omitempty"`
	Manual    bool              `json:"manual" doc:"Пресет этого сделать не может: строка для админа"`
	Error     string            `json:"error,omitempty" doc:"После применения: почему не получилось"`
}

// postureNode is a node as a preset reads it.
type postureNode struct {
	node     db.Node
	inbounds []db.Inbound
	spare    []string      // free TCP ports for an inbound that leaves 443 to the router
	sites    []scan.Result // the sites next to the server, the best first
	scanned  bool          // sites were looked for: none left means none
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
			taken := map[string]bool{}
			state := make([]int, len(routable))
			for i, e := range routable {
				names := lowerNames(proto.SNINames(e.t, n.Domain))
				ok, foreign := true, false
				if proto.Reality(e.t) {
					dest, _ := presets.Dest(e.t)
					ok = len(names) > 0 && dest != presets.DefaultDest
				}
				for _, name := range names {
					ok = ok && !taken[name] && !(proto.Reality(e.t) && scan.Weak(name))
					foreign = foreign || !slices.Contains(own, name)
				}
				if !ok {
					continue
				}
				for _, name := range names {
					taken[name] = true
				}
				if state[i] = keeps; proto.Reality(e.t) && foreign {
					state[i] = stranger
				}
			}
			// The panel's own names nobody has yet: first to the protocols with no name to
			// keep, then to those on a stranger's site, in the order of the list.
			picks := map[int]string{}
			for _, want := range []int{needs, stranger} {
				for i, e := range routable {
					if state[i] != want || !proto.Reality(e.t) {
						continue
					}
					for _, name := range own {
						if !taken[name] {
							picks[i], taken[name] = name, true
							break
						}
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
		if !c.Manual {
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

// postureSpare is how many loopback ports a plan looks for on a node: more than a node has
// inbounds on 443.
const postureSpare = 8

// posture loads the installation. With probe the panel's own names are checked as REALITY
// sites (a connection to the panel's own HTTPS for each); without, it only reads the
// database.
func (h *handlers) posture(ctx context.Context, probe bool) (posture, error) {
	var p posture
	nodes, err := h.d.Store.Q.ListNodes(ctx)
	if err != nil {
		return p, err
	}
	for _, n := range nodes {
		ins, err := h.d.Store.Q.ListNodeInbounds(ctx, n.ID)
		if err != nil {
			return p, err
		}
		pn := postureNode{node: n, inbounds: ins}
		m, err := domain.NodePorts(ctx, h.d.Store.Q, n)
		if err != nil {
			return p, err
		}
		for port := domain.SpareLo; port <= domain.SpareHi && len(pn.spare) < postureSpare; port++ {
			if m.Free(port, "tcp") {
				pn.spare = append(pn.spare, strconv.Itoa(port))
			}
		}
		p.nodes = append(p.nodes, pn)
	}
	if p.page, err = h.d.Settings.String(ctx, settings.KeySiteTemplate); err != nil {
		return p, err
	}
	seed, err := h.d.Settings.String(ctx, settings.KeyFrontSeed)
	if err != nil {
		return p, err
	}
	p.pagePick = frontpage.Pick(seed)
	p.panelPort = targets.PanelPort(ctx, h.d.Settings)
	deps := h.targetDeps(0)
	for _, key := range []string{settings.KeyDomain, settings.KeySubDomain} {
		name, err := h.d.Settings.String(ctx, key)
		if err != nil {
			return p, err
		}
		if name = strings.ToLower(strings.TrimSuffix(name, ".")); name == "" || slices.Contains(p.own, name) {
			continue
		}
		if probe && !deps.Own(ctx, name).OK {
			continue
		}
		p.own = append(p.own, name)
	}
	return p, nil
}

type postureOutput struct {
	Body struct {
		Current  string `json:"current" enum:"standard,secure,custom"`
		Secure   int    `json:"secure" doc:"Сколько изменений осталось до «Безопасного»"`
		Standard int    `json:"standard" doc:"Сколько изменений осталось до «Стандартного»"`
	}
}

type posturePlanInput struct {
	Preset    string `path:"preset" enum:"standard,secure"`
	Neighbors bool   `query:"neighbors" doc:"Протоколам, которым не хватило собственных имён панели, подобрать чужие сайты рядом с сервером"`
}

type posturePlanOutput struct {
	Body struct {
		Changes []PostureChange `json:"changes"`
	}
}

type postureApplyInput struct {
	Preset string `path:"preset" enum:"standard,secure"`
	Body   struct {
		Changes []PostureChange `json:"changes" maxItems:"500" doc:"Строки плана, как их показала панель"`
	}
}

func (h *handlers) registerPosture() {
	huma.Register(h.api, huma.Operation{OperationID: "get-security-preset", Method: http.MethodGet, Path: "/api/v1/security/presets", Summary: "На каком пресете безопасности стоит панель", Tags: []string{"settings"}}, h.getPosture)
	huma.Register(h.api, huma.Operation{OperationID: "plan-security-preset", Method: http.MethodPost, Path: "/api/v1/security/presets/{preset}/plan", Summary: "Что изменит пресет безопасности", Tags: []string{"settings"}}, h.planPosture)
	huma.Register(h.api, huma.Operation{OperationID: "apply-security-preset", Method: http.MethodPost, Path: "/api/v1/security/presets/{preset}/apply", Summary: "Применить пресет безопасности", Tags: []string{"settings"}}, h.applyPosture)
}

func (h *handlers) getPosture(ctx context.Context, _ *struct{}) (*postureOutput, error) {
	p, err := h.posture(ctx, false)
	if err != nil {
		return nil, err
	}
	out := &postureOutput{}
	out.Body.Current, out.Body.Secure, out.Body.Standard = p.current()
	return out, nil
}

// planPosture is the preset's list of changes. For "secure" with neighbors, a node whose
// REALITY protocols the panel's own names do not cover is scanned for sites next to it.
func (h *handlers) planPosture(ctx context.Context, in *posturePlanInput) (*posturePlanOutput, error) {
	if err := requireSession(ctx, "preset"); err != nil {
		return nil, err
	}
	p, err := h.posture(ctx, true)
	if err != nil {
		return nil, err
	}
	out := &posturePlanOutput{}
	if in.Preset == postureStandard {
		out.Body.Changes = changesOrNone(p.standard())
		return out, nil
	}
	p.neighbors = in.Neighbors
	need := map[int64]bool{}
	for _, c := range p.secure() {
		if c.Code == "site" && c.Params["dest"] == "" {
			need[c.Node] = true
		}
	}
	if len(need) > 0 {
		if !scanning.TryLock() {
			return nil, huma.Error409Conflict("scan_busy")
		}
		defer scanning.Unlock()
	}
	for i := range p.nodes {
		pn := &p.nodes[i]
		pn.scanned = true
		if !need[pn.node.ID] {
			continue
		}
		// A scan that fails leaves the node without sites: its protocols are named in the
		// plan as the admin's to settle.
		if found, err := h.targetDeps(pn.node.ID).Scan(ctx, targets.ScanRequest{Node: pn.node}); err == nil {
			pn.sites = found.Results
		}
	}
	out.Body.Changes = changesOrNone(p.secure())
	return out, nil
}

func changesOrNone(c []PostureChange) []PostureChange {
	if c == nil {
		return []PostureChange{}
	}
	return c
}

// applyPosture writes the changes the admin was shown: each inbound and each node in one
// write of its own, checked as a change made by hand is. A change that is refused is
// answered with its reason and the rest go on; a node whose protocol could not leave port
// 443 does not get the router that needs that port.
func (h *handlers) applyPosture(ctx context.Context, in *postureApplyInput) (*posturePlanOutput, error) {
	if err := requireSession(ctx, "preset"); err != nil {
		return nil, err
	}
	changes := slices.Clone(in.Body.Changes)
	on, off := true, false

	// The inbounds: everything a preset changes in one of them is one patch.
	var order []int64
	patches := map[int64]*domain.InboundPatch{}
	rows := map[int64][]int{}
	for i, c := range changes {
		if c.Manual || c.InboundID == 0 {
			continue
		}
		p := patches[c.InboundID]
		if p == nil {
			p = &domain.InboundPatch{}
			patches[c.InboundID] = p
			order = append(order, c.InboundID)
		}
		switch c.Code {
		case "site":
			dest, sni := c.Params["dest"], c.Params["sni"]
			p.Dest, p.ServerName = &dest, &sni
		case "port":
			port := c.Params["to"]
			p.Port = &port
		case "through_443":
			p.SNI443 = &on
		case "direct":
			p.SNI443 = &off
		default:
			return nil, fieldErr("body.changes", "bad_change")
		}
		rows[c.InboundID] = append(rows[c.InboundID], i)
	}
	stuck := map[int64]bool{} // nodes with an inbound left on port 443
	applied := 0
	for _, id := range order {
		if _, _, err := h.d.Inbounds.Update(ctx, id, *patches[id]); err != nil {
			for _, i := range rows[id] {
				changes[i].Error = errCode(err)
				stuck[changes[i].Node] = stuck[changes[i].Node] || changes[i].Code == "port"
			}
			continue
		}
		applied++
	}

	// The nodes.
	var nodeOrder []int64
	nodePatches := map[int64]*domain.NodePatch{}
	nodeRows := map[int64][]int{}
	for i, c := range changes {
		if c.Manual || c.InboundID != 0 || c.Code == "panel_page" {
			continue
		}
		p := nodePatches[c.Node]
		if p == nil {
			p = &domain.NodePatch{}
			nodePatches[c.Node] = p
			nodeOrder = append(nodeOrder, c.Node)
		}
		switch c.Code {
		case "router_on":
			if stuck[c.Node] {
				changes[i].Error = "port_443_busy"
				continue
			}
			p.SNIRouter = &on
		case "router_off":
			p.SNIRouter = &off
		case "api_lockdown":
			p.APILockdown = &on
		case "front_page":
			page := c.Params["page"]
			p.FrontPage = &page
		default:
			return nil, fieldErr("body.changes", "bad_change")
		}
		nodeRows[c.Node] = append(nodeRows[c.Node], i)
	}
	for _, id := range nodeOrder {
		if len(nodeRows[id]) == 0 {
			continue
		}
		if _, err := domain.UpdateNode(ctx, h.d.Store, id, *nodePatches[id], h.d.Now()); err != nil {
			for _, i := range nodeRows[id] {
				changes[i].Error = errCode(err)
			}
			continue
		}
		applied++
	}

	// The panel's own page.
	for i, c := range changes {
		if c.Manual || c.Code != "panel_page" {
			continue
		}
		page := c.Params["page"]
		if frontpage.Page(page) == nil {
			changes[i].Error = "bad_front_page"
			continue
		}
		if err := settings.Set(ctx, h.d.Settings, settings.KeySiteTemplate, page); err != nil {
			return nil, err
		}
		applied++
	}
	if applied > 0 {
		h.nodesChanged()
		h.d.Changes.SlotsChanged()
	}
	failed := 0
	for _, c := range changes {
		if c.Error != "" {
			failed++
		}
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "security.preset", "settings", in.Preset, map[string]any{"changes": len(changes), "failed": failed})
	out := &posturePlanOutput{}
	out.Body.Changes = changesOrNone(changes)
	return out, nil
}

// errCode is a refusal's word for a line of the plan: the domain's own where it has one,
// the error's text otherwise.
func errCode(err error) string {
	var edit *domain.EditError
	if errors.As(err, &edit) {
		return edit.Err.Error()
	}
	return err.Error()
}
