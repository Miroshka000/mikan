package api

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
)

type TopUser struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
}

type overviewOutput struct {
	Body struct {
		Online        int       `json:"online"`
		UsersTotal    int       `json:"users_total"`
		UsersActive   int       `json:"users_active"`
		Expiring7d    int       `json:"expiring_7d"`
		TrafficToday  int64     `json:"traffic_today"`
		TrafficYest   int64     `json:"traffic_yesterday"`
		Top           []TopUser `json:"top_month"`
		SlotsFree     int64     `json:"slots_free"`
		SlotsAssigned int64     `json:"slots_assigned"`
		GeneratedAt   time.Time `json:"generated_at"`
	}
}

type rangeInput struct {
	Range string `query:"range" enum:"24h,7d,30d" default:"24h"`
}

type NodeView struct {
	OK        bool                     `json:"ok"`
	Error     string                   `json:"error,omitempty"`
	Version   string                   `json:"version"`
	Core      string                   `json:"core"`
	StartedAt *time.Time               `json:"started_at,omitempty"`
	Conns     int                      `json:"conns"`
	System    nodeapi.System           `json:"system"`
	Listeners []nodeapi.ListenerStatus `json:"listeners"`
	CheckedAt time.Time                `json:"checked_at"`
}

type nodeOutput struct{ Body NodeView }

func (h *handlers) registerStats() {
	huma.Register(h.api, huma.Operation{OperationID: "stats-overview", Method: http.MethodGet, Path: "/api/v1/stats/overview", Summary: "Показатели дашборда", Tags: []string{"stats"}}, h.overview)
	huma.Register(h.api, huma.Operation{OperationID: "stats-traffic", Method: http.MethodGet, Path: "/api/v1/stats/traffic", Summary: "Трафик сервера", Tags: []string{"stats"}}, h.serverTraffic)
	huma.Register(h.api, huma.Operation{OperationID: "node-health", Method: http.MethodGet, Path: "/api/v1/node", Summary: "Состояние ноды", Tags: []string{"node"}}, h.nodeHealth)
}

func (h *handlers) overview(ctx context.Context, _ *struct{}) (*overviewOutput, error) {
	now := h.d.Now()
	out := &overviewOutput{}
	b := &out.Body
	b.GeneratedAt = now.UTC()
	// Counted by the database: the dashboard does not load every user.
	counts, err := domain.CountStates(ctx, h.d.Store.Q, now)
	if err != nil {
		return nil, err
	}
	b.UsersTotal, b.UsersActive, b.Expiring7d = int(counts.Total), int(counts.Active+counts.Expiring), int(counts.Expiring)
	if b.Online, err = h.onlineUsers(ctx); err != nil {
		return nil, err
	}
	today := now.Unix() / 86400
	days, err := h.d.Store.Q.TotalTrafficDaily(ctx, today-1)
	if err != nil {
		return nil, err
	}
	for _, d := range days {
		switch d.Day {
		case today:
			b.TrafficToday = d.Up + d.Down
		case today - 1:
			b.TrafficYest = d.Up + d.Down
		}
	}
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).Unix() / 86400
	top, err := h.d.Store.Q.TopUsersByTraffic(ctx, db.TopUsersByTrafficParams{Day: monthStart, Lim: 5})
	if err != nil {
		return nil, err
	}
	b.Top = make([]TopUser, 0, len(top))
	for _, t := range top {
		b.Top = append(b.Top, TopUser{ID: t.ID, Name: t.Name, Bytes: t.Bytes})
	}
	if h.d.Pool != nil {
		ps, err := h.d.Pool.Stats(ctx)
		if err != nil {
			return nil, err
		}
		b.SlotsFree, b.SlotsAssigned = ps.Free, ps.Assigned
	}
	return out, nil
}

// trafficSince is where a range of the traffic charts starts: a day number for 30 days
// (the charts then go by the day), an hour number otherwise (by the hour).
func trafficSince(now time.Time, rng string) (daily bool, since int64) {
	switch rng {
	case "30d":
		return true, now.Add(-30*24*time.Hour).Unix() / 86400
	case "7d":
		return false, now.Add(-7*24*time.Hour).Unix() / 3600
	}
	return false, now.Add(-24*time.Hour).Unix() / 3600
}

func (h *handlers) serverTraffic(ctx context.Context, in *rangeInput) (*trafficOutput, error) {
	out := &trafficOutput{}
	out.Body.Points = []TrafficPoint{}
	daily, since := trafficSince(h.d.Now(), in.Range)
	if daily {
		rows, err := h.d.Store.Q.TotalTrafficDaily(ctx, since)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			out.Body.Points = append(out.Body.Points, TrafficPoint{T: time.Unix(r.Day*86400, 0).UTC(), Up: r.Up, Down: r.Down})
		}
		return out, nil
	}
	rows, err := h.d.Store.Q.TotalTrafficHourly(ctx, since)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out.Body.Points = append(out.Body.Points, TrafficPoint{T: time.Unix(r.Hour*3600, 0).UTC(), Up: r.Up, Down: r.Down})
	}
	return out, nil
}

type nodeHealthInput struct {
	ID int64 `query:"id" default:"1" minimum:"1" doc:"Нода; 1 — своя нода панели"`
}

func (h *handlers) nodeHealth(ctx context.Context, in *nodeHealthInput) (*nodeOutput, error) {
	out := &nodeOutput{}
	if h.d.Nodes == nil {
		out.Body = NodeView{Error: "node sync disabled", Listeners: []nodeapi.ListenerStatus{}}
		return out, nil
	}
	hv, ok := h.d.Nodes.Health(in.ID)
	if !ok {
		hv.Error = "not connected"
	}
	v := NodeView{OK: hv.OK, Error: hv.Error, Version: hv.Health.Version, Core: hv.Health.Core, Conns: hv.Health.Conns,
		System: hv.Health.System, Listeners: hv.Listeners, CheckedAt: hv.CheckedAt}
	if !hv.Health.StartedAt.IsZero() {
		t := hv.Health.StartedAt
		v.StartedAt = &t
	}
	if v.Listeners == nil {
		v.Listeners = []nodeapi.ListenerStatus{}
	}
	out.Body = v
	return out, nil
}

// onlineUsers is how many users have a connection now. It counts users: one with several
// bound devices has several slots online.
func (h *handlers) onlineUsers(ctx context.Context) (int, error) {
	online := h.online()
	if len(online) == 0 {
		return 0, nil
	}
	names := make([]string, 0, len(online))
	for n := range online {
		names = append(names, n)
	}
	owners, err := h.d.Store.Q.SlotOwners(ctx, names)
	if err != nil {
		return 0, err
	}
	seen := map[int64]bool{}
	for _, o := range owners {
		seen[o.UserID] = true
	}
	return len(seen), nil
}
