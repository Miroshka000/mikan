package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/nodesync"
	"mikan/internal/panel/store/db"
)

type CascadeHop struct {
	NodeID int64  `json:"node_id"`
	Name   string `json:"name"`
}

type ProbeView struct {
	OK        bool      `json:"ok"`
	IP        string    `json:"ip,omitempty" doc:"Адрес, который видят сайты"`
	Colo      string    `json:"colo,omitempty"`
	Error     string    `json:"error,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
}

type CascadeExit struct {
	CascadeHop
	Inbounds []string   `json:"inbounds" doc:"Подключения этой ноды, которые выходят через ту ноду"`
	Relay    bool       `json:"relay" doc:"Через неё идёт и трафик, который другие ноды передают через эту"`
	Probe    *ProbeView `json:"probe,omitempty" doc:"Проверка с этой ноды: какой IP видят сайты через цепочку"`
}

// The states of a relay's listener on the node (CascadeRelay.Listener).
const (
	relayUp      = "ok"
	relayBusy    = "busy"    // another program holds the port
	relayFailed  = "failed"  // it could not start, for another reason
	relayUnknown = "unknown" // the node does not answer, or has not applied the relay yet
)

type CascadeRelay struct {
	Port       string       `json:"port"`
	Sources    []CascadeHop `json:"sources" doc:"Ноды, которые выходят через эту"`
	Outbound   string       `json:"outbound" enum:"direct,warp,node" doc:"Куда эта нода выпускает их трафик"`
	ExitNodeID *int64       `json:"exit_node_id,omitempty"`
	Listener   string       `json:"listener" enum:"ok,busy,failed,unknown" doc:"Слушает ли нода порт служебного входа: busy, если порт занят другой программой"`
	Error      string       `json:"error,omitempty" doc:"Почему служебный вход не запустился, как сказала нода"`
}

type CascadeView struct {
	Relay *CascadeRelay `json:"relay,omitempty" doc:"Служебный вход для других нод; есть, когда кто-то выходит через эту ноду"`
	Exits []CascadeExit `json:"exits" doc:"Ноды, через которые эта нода выпускает трафик"`
}

type cascadeOutput struct{ Body CascadeView }

type cascadePatchInput struct {
	ID   int64 `path:"id" minimum:"1"`
	Body struct {
		Outbound   string `json:"outbound" enum:"direct,warp,node" doc:"Куда выпускать трафик других нод, пришедший через эту"`
		ExitNodeID int64  `json:"exit_node_id,omitempty" minimum:"0"`
	}
}

func (h *handlers) registerCascade() {
	tags := []string{"node"}
	huma.Register(h.api, huma.Operation{OperationID: "get-node-cascade", Method: http.MethodGet, Path: "/api/v1/nodes/{id}/cascade", Summary: "Каскад ноды: выходы и служебный вход", Tags: tags}, h.getCascade)
	huma.Register(h.api, huma.Operation{OperationID: "update-node-cascade", Metadata: sessionOnly, Extensions: sessionOnlyExt, Method: http.MethodPatch, Path: "/api/v1/nodes/{id}/cascade", Summary: "Куда нода выпускает трафик других нод", Tags: tags}, h.patchCascade)
}

// cascadeRefusals are the domain's refusals of an exit the API reports by their own code.
var cascadeRefusals = []error{domain.ErrExitSelf, domain.ErrExitCycle, domain.ErrExitLong, domain.ErrExitOff, domain.ErrNoPort}

// cascadeError maps the domain's refusals to the API's codes.
func cascadeError(err error, field string) error {
	for _, e := range cascadeRefusals {
		if errors.Is(err, e) {
			return huma.Error422UnprocessableEntity("cascade", &huma.ErrorDetail{Location: "body." + field, Message: e.Error()})
		}
	}
	if errors.Is(err, domain.ErrNotFound) {
		return huma.Error422UnprocessableEntity("cascade", &huma.ErrorDetail{Location: "body." + field, Message: "exit_not_found"})
	}
	return err
}

// relayListener is the state of the relay's listener in a health check, and the node's
// words when it failed. A node that runs another port than the row's has not caught up
// with a move yet: what it says is not about this port.
func relayListener(hv nodesync.HealthView, port string) (state, why string) {
	if !hv.OK || hv.Ports[nodeapi.RelayListener] != port {
		return relayUnknown, ""
	}
	for _, l := range hv.Listeners {
		switch {
		case l.Name != nodeapi.RelayListener:
		case l.OK:
			return relayUp, ""
		case l.Busy():
			return relayBusy, l.Error
		default:
			return relayFailed, l.Error
		}
	}
	return relayUnknown, ""
}

// hostOf is what a node last said listens on its server; nil when nobody knows.
func (h *handlers) hostOf(id int64) *nodeapi.HostPorts {
	if h.d.Nodes == nil {
		return nil
	}
	if hv, ok := h.d.Nodes.Health(id); ok {
		return hv.HostPorts()
	}
	return nil
}

func (h *handlers) getCascade(ctx context.Context, in *nodeIDInput) (*cascadeOutput, error) {
	if _, err := h.getNode(ctx, in.ID); err != nil {
		return nil, err
	}
	q := h.d.Store.Q
	nodes, err := q.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	names := map[int64]string{}
	for _, n := range nodes {
		names[n.ID] = n.Name
	}
	ins, err := q.ListInbounds(ctx)
	if err != nil {
		return nil, err
	}
	out := &cascadeOutput{Body: CascadeView{Exits: []CascadeExit{}}}
	r, err := q.GetNodeRelay(ctx, in.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	// The relay runs, and goes on to its exit, only while some node leaves through it now.
	live := false
	if err == nil {
		users, err := q.ListRelayUsers(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		relays, err := q.ListNodeRelays(ctx)
		if err != nil {
			return nil, err
		}
		users = domain.NewCascade(nodes, ins, relays).RelaySources(in.ID, users)
		live = len(users) > 0
		v := &CascadeRelay{Port: r.Port, Sources: []CascadeHop{}, Outbound: r.Outbound, Listener: relayUnknown}
		out.Body.Relay = v
		for _, u := range users {
			v.Sources = append(v.Sources, CascadeHop{NodeID: u.SrcNodeID, Name: names[u.SrcNodeID]})
		}
		if r.ExitNodeID.Valid {
			id := r.ExitNodeID.Int64
			v.Outbound, v.ExitNodeID = "node", &id
		}
		if live && h.d.Nodes != nil {
			if hv, ok := h.d.Nodes.Health(in.ID); ok {
				v.Listener, v.Error = relayListener(hv, r.Port)
			}
		}
	}
	idx := map[int64]int{}
	exit := func(id int64) *CascadeExit {
		if i, ok := idx[id]; ok {
			return &out.Body.Exits[i]
		}
		idx[id] = len(out.Body.Exits)
		out.Body.Exits = append(out.Body.Exits, CascadeExit{CascadeHop: CascadeHop{NodeID: id, Name: names[id]}, Inbounds: []string{}})
		return &out.Body.Exits[len(out.Body.Exits)-1]
	}
	for _, i := range ins {
		if i.NodeID == in.ID && i.ExitNodeID.Valid {
			e := exit(i.ExitNodeID.Int64)
			e.Inbounds = append(e.Inbounds, i.Name)
		}
	}
	if live && r.ExitNodeID.Valid {
		exit(r.ExitNodeID.Int64).Relay = true
	}
	if h.d.Nodes != nil {
		for i := range out.Body.Exits {
			e := &out.Body.Exits[i]
			if p, err := h.d.Nodes.Probe(ctx, in.ID, nodeapi.ExitName(e.NodeID)); err == nil {
				e.Probe = &ProbeView{OK: p.OK, IP: p.IP, Colo: p.Colo, Error: p.Error, CheckedAt: p.CheckedAt}
			}
		}
	}
	return out, nil
}

func (h *handlers) patchCascade(ctx context.Context, in *cascadePatchInput) (*cascadeOutput, error) {
	if _, err := h.getNode(ctx, in.ID); err != nil {
		return nil, err
	}
	b := in.Body
	err := h.d.Store.Tx(ctx, func(q *db.Queries) error {
		// The relay is made even before anyone uses the node as an exit: the route is
		// ready when they do.
		n, err := q.GetNode(ctx, in.ID)
		if err != nil {
			return err
		}
		if _, err := domain.EnsureRelay(ctx, q, n, h.d.Now(), h.hostOf(n.ID)); err != nil {
			return err
		}
		outbound, exit := b.Outbound, sql.NullInt64{}
		if outbound == "node" {
			if b.ExitNodeID == 0 {
				return domain.ErrNotFound
			}
			if err := domain.UseExit(ctx, q, in.ID, b.ExitNodeID, h.d.Now(), h.hostOf); err != nil {
				return err
			}
			outbound, exit = "direct", sql.NullInt64{Int64: b.ExitNodeID, Valid: true}
		}
		return q.SetNodeRelayRoute(ctx, db.SetNodeRelayRouteParams{Outbound: outbound, ExitNodeID: exit, NodeID: in.ID})
	})
	if err != nil {
		return nil, cascadeError(err, "exit_node_id")
	}
	h.d.Changes.SlotsChanged()
	h.audit(ctx, sessionOf(ctx).AdminID, "node.cascade", "node", strconv.FormatInt(in.ID, 10), map[string]any{"outbound": b.Outbound, "exit_node_id": b.ExitNodeID})
	return h.getCascade(ctx, &nodeIDInput{ID: in.ID})
}
