package panelapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"mikan/internal/panel/audit"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/presets"
	"mikan/internal/panel/store/db"
	"mikan/internal/proto"
)

// The nodes and their protocols, for addons that look after the servers: nodes:read and
// inbounds:read see them without addresses, keys or secrets; inbounds:write turns a protocol
// on and off and moves its port and SNI, each change audited under the addon's name, as the
// automatic fixes of the panel do.

// NodeState is what the panel knows of a node now; Known false while it has not asked.
type NodeState struct {
	Known    bool
	OK       bool
	Version  string
	Conns    int
	Statuses map[string]bool // listener name → it runs
}

func (s *Server) handleInfra() {
	s.Handle(http.MethodGet, "/nodes", "nodes:read", s.nodesList)
	s.Handle(http.MethodGet, "/inbounds", "inbounds:read", s.inboundsList)
	s.Handle(http.MethodPatch, "/inbounds/{id}", "inbounds:write", s.inboundPatch)
}

type nodeView struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	PublicName string `json:"public_name"`
	Local      bool   `json:"local"` // the panel's own node
	Enabled    bool   `json:"enabled"`
	Status     string `json:"status"` // ok, error, unknown
	Version    string `json:"version,omitempty"`
	Conns      int    `json:"conns"`
}

func (s *Server) nodesList(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.d.Store.Q.ListNodes(r.Context())
	if err != nil {
		s.internal(w, "nodes", err)
		return
	}
	out := make([]nodeView, 0, len(nodes))
	for _, n := range nodes {
		v := nodeView{ID: n.ID, Name: n.Name, PublicName: n.PublicName, Local: n.Address == "", Enabled: n.Enabled != 0, Status: "unknown"}
		if st := s.nodeState(n.ID); st.Known {
			v.Status, v.Version, v.Conns = "error", st.Version, st.Conns
			if st.OK {
				v.Status = "ok"
			}
		}
		out = append(out, v)
	}
	Write(w, http.StatusOK, out)
}

func (s *Server) nodeState(id int64) NodeState {
	if s.d.NodeState == nil {
		return NodeState{}
	}
	return s.d.NodeState(id)
}

type inboundView struct {
	ID          int64    `json:"id"`
	NodeID      int64    `json:"node_id"`
	Name        string   `json:"name"`
	Title       string   `json:"title"` // the name clients see
	Preset      string   `json:"preset"`
	Type        string   `json:"type"`
	Network     string   `json:"network"`
	Port        string   `json:"port"`
	Dest        string   `json:"dest,omitempty"`         // REALITY: the site it passes for
	ServerNames []string `json:"server_names,omitempty"` // the SNI it takes
	Enabled     bool     `json:"enabled"`
	Status      string   `json:"status"` // ok, error, off, unknown
}

func (s *Server) inboundsList(w http.ResponseWriter, r *http.Request) {
	rows, err := s.d.Store.Q.ListInbounds(r.Context())
	if err != nil {
		s.internal(w, "inbounds", err)
		return
	}
	out := make([]inboundView, 0, len(rows))
	for _, in := range rows {
		out = append(out, s.inboundView(in))
	}
	Write(w, http.StatusOK, out)
}

func (s *Server) inboundView(in db.Inbound) inboundView {
	v := inboundView{ID: in.ID, NodeID: in.NodeID, Name: in.Name, Title: domain.ProxyName(in), Preset: in.Preset, Port: in.Port, Enabled: in.Enabled != 0, Status: "unknown"}
	if t, err := proto.Parse(in.Config); err == nil {
		v.Type, v.Network = t.Type(), t.Network()
		v.Dest, v.ServerNames = presets.Dest(t)
	}
	switch st := s.nodeState(in.NodeID); {
	case !v.Enabled:
		v.Status = "off"
	case !st.Known:
	case st.Statuses[in.Name]:
		v.Status = "ok"
	default:
		v.Status = "error"
	}
	return v
}

// inboundPatchBody: what an addon may change, each optional.
type inboundPatchBody struct {
	Enabled    *bool   `json:"enabled"`
	Port       *string `json:"port"`
	ServerName *string `json:"server_name"`
	Reason     string  `json:"reason"`
}

func (s *Server) inboundPatch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, _ := CallerOf(ctx)
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		Fail(w, http.StatusBadRequest, "bad_request", "the id is a number")
		return
	}
	var b inboundPatchBody
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		Fail(w, http.StatusBadRequest, "bad_request", "the body is JSON: enabled, port, server_name, reason")
		return
	}
	if b.Enabled == nil && b.Port == nil && b.ServerName == nil {
		Fail(w, http.StatusBadRequest, "bad_request", "enabled, port or server_name is needed")
		return
	}
	if len([]rune(b.Reason)) > maxReason {
		Fail(w, http.StatusBadRequest, "bad_request", "reason is up to 200 characters")
		return
	}
	p := domain.InboundPatch{Enabled: b.Enabled, Port: b.Port, ServerName: b.ServerName}
	prev, next, err := s.d.Inbounds.Update(ctx, id, p)
	var edit *domain.EditError
	var pe *proto.Error
	var busy *domain.PortInUseError
	switch {
	case errors.Is(err, domain.ErrUnknownInbound):
		Fail(w, http.StatusNotFound, "not_found", "no such inbound")
		return
	case errors.Is(err, domain.ErrBadPort):
		Fail(w, http.StatusBadRequest, "bad_request", "port is 1 to 65535 or a range")
		return
	case errors.Is(err, domain.ErrPortRangeWide):
		Fail(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("a port range is at most %d ports", domain.MaxHopPorts))
		return
	case errors.Is(err, domain.ErrInboundChanged):
		Fail(w, http.StatusConflict, "conflict", "the inbound changed meanwhile; read it again")
		return
	case errors.As(err, &edit), errors.As(err, &pe):
		Fail(w, http.StatusBadRequest, "bad_request", strings.TrimSpace(err.Error()))
		return
	case errors.As(err, &busy):
		Fail(w, http.StatusConflict, "conflict", "the port is taken")
		return
	case err != nil:
		s.internal(w, "inbound patch", err)
		return
	}
	details := map[string]any{"reason": b.Reason}
	if b.Enabled != nil {
		details["enabled"] = next.Enabled != 0
	}
	if b.Port != nil {
		details["port"], details["old_port"] = next.Port, prev.Port
	}
	if b.ServerName != nil {
		details["server_name"] = *b.ServerName
	}
	details["actor"] = audit.AddonActor(c.ID)
	if err := audit.Write(ctx, s.d.Store.Q, s.d.Now(), audit.Entry{Action: "inbound.update", TargetType: "inbound", TargetID: next.Name, Details: details}); err != nil {
		s.d.Log.Warn("addon inbound audit", "addon", c.ID, "err", err)
	}
	if s.d.Changes != nil {
		s.d.Changes.SlotsChanged()
	}
	Write(w, http.StatusOK, s.inboundView(next))
}
