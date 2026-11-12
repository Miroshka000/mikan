package api

import (
	"context"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/nodesync"
	"mikan/internal/panel/store/db"
)

// SNIClashView is what an inbound's name shares behind its node's shared 443 with others the
// node's router cannot tell from it: one of them gets the connections, the rest none.
type SNIClashView struct {
	With []string `json:"with" doc:"Подключения ноды, которые спрашивают то же имя за общим 443"`
	Fix  string   `json:"fix" enum:"by_key,update,name" doc:"Что поможет: by_key — включить у ноды «Одно имя для протоколов REALITY», update — обновить ноду (она старше этой настройки), name — дать подключению другое имя"`
}

// sniClashes finds, by inbound id, the inbounds behind their node's router that clash with
// others on a name: of one node, of every node with node 0.
func (h *handlers) sniClashes(ctx context.Context, node int64) (map[int64]*SNIClashView, error) {
	nodes, err := h.d.Store.Q.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	out := map[int64]*SNIClashView{}
	for _, n := range nodes {
		if n.SniRouter == 0 || node != 0 && n.ID != node {
			continue
		}
		ins, err := h.d.Store.Q.ListNodeInbounds(ctx, n.ID)
		if err != nil {
			return nil, err
		}
		for id, c := range domain.SNIClashes(ins, n.Domain, n.SniByKey != 0 && h.routesByKey(n)) {
			fix := c.Fix
			if fix == "by_key" && n.SniByKey != 0 {
				fix = "update"
			}
			out[id] = &SNIClashView{With: c.With, Fix: fix}
		}
	}
	return out, nil
}

// withSNIClash adds the clash of one inbound to its view. It only warns: a failure to read
// leaves the view as it is.
func (h *handlers) withSNIClash(ctx context.Context, v *InboundView) {
	if c, err := h.sniClashes(ctx, v.NodeID); err == nil {
		v.SNIClash = c[v.ID]
	}
}

// routesByKey says whether a node's router tells REALITY inbounds that share a name apart:
// the panel's own node runs the panel's build, another goes by the version it reported.
func (h *handlers) routesByKey(n db.Node) bool {
	if n.Address == "" {
		return true
	}
	if h.d.Nodes == nil {
		return false
	}
	hv, ok := h.d.Nodes.Health(n.ID)
	return ok && nodesync.RoutesByKey(hv.Health.Version)
}
