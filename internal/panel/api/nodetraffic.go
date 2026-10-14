package api

import (
	"cmp"
	"context"
	"net/http"
	"slices"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
)

// Traffic by node: the bytes of the users' statistics, grouped by the node that carried
// them (nodesync counts both on one transaction, so the nodes add up to the users).

// NodeShare is what one node carried in a range.
type NodeShare struct {
	ID    int64  `json:"id"`
	Name  string `json:"name" doc:"Имя ноды; у своей ноды панели может быть пустым"`
	Local bool   `json:"local" doc:"Своя нода панели"`
	Up    int64  `json:"up"`
	Down  int64  `json:"down"`
	Bytes int64  `json:"bytes" doc:"up + down"`
}

type nodeSharesOutput struct {
	Body struct {
		Items []NodeShare `json:"items" doc:"Все ноды, больше всех сверху"`
		Total int64       `json:"total" doc:"Сколько унесли все ноды вместе"`
		// Since is set when the period starts before the per-node accounting does: the
		// totals then cover less than the period, and a shorter period shows the same.
		Since *time.Time `json:"since,omitempty" doc:"Учёт по нодам начался позже начала периода: день, с которого он идёт"`
	}
}

func (h *handlers) registerNodeTraffic() {
	huma.Register(h.api, huma.Operation{OperationID: "node-traffic", Method: http.MethodGet, Path: "/api/v1/nodes/{id}/traffic", Summary: "График трафика ноды", Tags: []string{"node"}}, h.nodeTraffic)
	huma.Register(h.api, huma.Operation{OperationID: "stats-nodes", Method: http.MethodGet, Path: "/api/v1/stats/nodes", Summary: "Трафик по нодам",
		Description: "Сколько унесла каждая нода за период. Учёт идёт с версии, где он появился: прошлое по нодам не восстанавливается.",
		Tags:        []string{"stats"}}, h.nodeShares)
}

func (h *handlers) nodeTraffic(ctx context.Context, in *trafficInput) (*trafficOutput, error) {
	if _, err := h.getNode(ctx, in.ID); err != nil {
		return nil, err
	}
	out := &trafficOutput{}
	out.Body.Points = []TrafficPoint{}
	daily, since := trafficSince(h.d.Now(), in.Range)
	if daily {
		rows, err := h.d.Store.Q.NodeTrafficDaily(ctx, db.NodeTrafficDailyParams{NodeID: in.ID, Day: since})
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			out.Body.Points = append(out.Body.Points, TrafficPoint{T: time.Unix(r.Day*86400, 0).UTC(), Up: r.Up, Down: r.Down})
		}
		return out, nil
	}
	rows, err := h.d.Store.Q.NodeTrafficHourly(ctx, db.NodeTrafficHourlyParams{NodeID: in.ID, Hour: since})
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out.Body.Points = append(out.Body.Points, TrafficPoint{T: time.Unix(r.Hour*3600, 0).UTC(), Up: r.Up, Down: r.Down})
	}
	return out, nil
}

// nodeBytes is what each node carried in a range, by node id.
func (h *handlers) nodeBytes(ctx context.Context, rng string) (map[int64]domain.Bytes, error) {
	daily, since := trafficSince(h.d.Now(), rng)
	out := map[int64]domain.Bytes{}
	if daily {
		rows, err := h.d.Store.Q.SumNodesTrafficDaily(ctx, since)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			out[r.NodeID] = domain.Bytes{Up: r.Up, Down: r.Down}
		}
		return out, nil
	}
	rows, err := h.d.Store.Q.SumNodesTrafficHourly(ctx, since)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.NodeID] = domain.Bytes{Up: r.Up, Down: r.Down}
	}
	return out, nil
}

func (h *handlers) nodeShares(ctx context.Context, in *rangeInput) (*nodeSharesOutput, error) {
	nodes, err := h.d.Store.Q.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	by, err := h.nodeBytes(ctx, in.Range)
	if err != nil {
		return nil, err
	}
	out := &nodeSharesOutput{}
	out.Body.Items = make([]NodeShare, 0, len(nodes))
	for _, n := range nodes {
		t := by[n.ID]
		out.Body.Items = append(out.Body.Items, NodeShare{ID: n.ID, Name: n.Name, Local: n.Address == "", Up: t.Up, Down: t.Down, Bytes: t.Up + t.Down})
		out.Body.Total += t.Up + t.Down
	}
	// Biggest first; equal ones keep the subscription's order (ListNodes already has it).
	slices.SortStableFunc(out.Body.Items, func(a, b NodeShare) int { return cmp.Compare(b.Bytes, a.Bytes) })
	first, err := h.d.Store.Q.FirstNodeTrafficDay(ctx)
	if err != nil {
		return nil, err
	}
	if start := time.Unix(first*86400, 0).UTC(); first > 0 && start.After(periodStart(h.d.Now(), in.Range)) {
		out.Body.Since = &start
	}
	return out, nil
}

// periodStart is the first moment trafficSince counts for rng.
func periodStart(now time.Time, rng string) time.Time {
	daily, since := trafficSince(now, rng)
	if daily {
		return time.Unix(since*86400, 0).UTC()
	}
	return time.Unix(since*3600, 0).UTC()
}
