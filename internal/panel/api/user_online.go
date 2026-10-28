package api

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strconv"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/nodeapi"
)

// UserOnlineView is where a user is online now: one node and one set of keys (the user's own,
// or a bound device's), as that node last reported it.
type UserOnlineView struct {
	NodeID   int64  `json:"node_id"`
	NodeName string `json:"node_name"`
	// DeviceID is the bound device whose keys are online; nil: the user's own keys (apps
	// without a device id, or the panel binds no devices).
	DeviceID *int64   `json:"device_id" doc:"Привязанное устройство, чьими ключами подключены; null — общие ключи пользователя (приложения без ID или без привязки)"`
	IPs      []string `json:"online_ips" doc:"Адреса с открытыми соединениями на этой ноде; оператор поддержки без доступа к подписчикам получает пустой список"`
	Conns    int      `json:"conns" doc:"Открытые соединения на этой ноде"`
}

type userOnlineOutput struct{ Body []UserOnlineView }

func (h *handlers) registerUserOnline(tags []string) {
	huma.Register(h.api, huma.Operation{OperationID: "user-online", Method: http.MethodGet, Path: "/api/v1/users/{id}/online",
		Summary: "Где пользователь онлайн сейчас: ноды, устройства, адреса", Tags: tags}, h.userOnline)
	huma.Register(h.api, huma.Operation{OperationID: "reset-user-connections", Method: http.MethodPost, Path: "/api/v1/users/{id}/reset-connections",
		Summary: "Сбросить соединения: ноды закрывают открытые соединения, доступ остаётся, приложения подключаются заново", Tags: tags}, h.resetUserConnections)
}

// userOnline splits what the user list merges (online, online_ips) by node and device,
// from the nodes' last counters (every few seconds); resetUserConnections closes them. The
// field of addresses is online_ips, which the people mask empties for an operator who may
// not see the subscribers.
func (h *handlers) userOnline(ctx context.Context, in *userIDInput) (*userOnlineOutput, error) {
	if _, err := h.d.Users.Get(ctx, in.ID); err != nil {
		return nil, mapDomainErr(err)
	}
	out := &userOnlineOutput{Body: []UserOnlineView{}}
	if h.d.NodeOnline == nil {
		return out, nil
	}
	slots, err := h.d.Store.Q.ListUserSlots(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	rows, err := h.d.Store.Q.ListBoundDeviceSlots(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	device := make(map[string]int64, len(rows))
	for _, r := range rows {
		device[r.SlotName] = r.DeviceID
	}
	nodes, err := h.d.Store.Q.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	name := make(map[int64]string, len(nodes))
	for _, n := range nodes {
		name[n.ID] = n.Name
	}
	for id, online := range h.d.NodeOnline() {
		for _, s := range slots {
			on, ok := online[s]
			if !ok || (len(on.IPs) == 0 && on.Conns == 0) {
				continue
			}
			v := UserOnlineView{NodeID: id, NodeName: name[id], IPs: slices.Sorted(slices.Values(on.IPs)), Conns: on.Conns}
			if v.IPs == nil {
				v.IPs = []string{}
			}
			if d, ok := device[s]; ok {
				v.DeviceID = &d
			}
			out.Body = append(out.Body, v)
		}
	}
	// By node, then the user's own keys before the devices', then by device.
	sort.Slice(out.Body, func(i, j int) bool {
		a, b := out.Body[i], out.Body[j]
		if a.NodeID != b.NodeID {
			return a.NodeID < b.NodeID
		}
		if (a.DeviceID == nil) != (b.DeviceID == nil) {
			return a.DeviceID == nil
		}
		return a.DeviceID != nil && *a.DeviceID < *b.DeviceID
	})
	return out, nil
}

type resetConnectionsInput struct {
	ID   int64 `path:"id" minimum:"1"`
	Body struct {
		NodeID int64 `json:"node_id,omitempty" minimum:"0" doc:"Нода; 0 или не передано — все ноды"`
		// DeviceID picks the keys: nil all of the user's, 0 the user's own, else a bound device's.
		DeviceID *int64 `json:"device_id,omitempty" minimum:"0" doc:"Чьи ключи: не передано — все ключи пользователя; 0 — общие ключи пользователя; иначе привязанное устройство (device_id из списка онлайн)"`
	}
}

// ConnResetView is how the reset went on one node.
type ConnResetView struct {
	NodeID   int64  `json:"node_id"`
	NodeName string `json:"node_name"`
	Status   string `json:"status" enum:"ok,node_too_old,node_unavailable" doc:"ok — соединения закрыты; node_too_old — нода старее 0.5.2.1 и сбрасывать не умеет, её пропустили; node_unavailable — нода не ответила"`
	Closed   int    `json:"closed" doc:"Сколько соединений закрыла нода"`
}

type resetConnectionsOutput struct {
	Body struct {
		Closed int             `json:"closed" doc:"Сколько соединений закрыто на всех нодах"`
		Nodes  []ConnResetView `json:"nodes" doc:"Как прошло на каждой ноде; нода, которая сбрасывать не умеет или не ответила, не делает ошибкой весь запрос"`
	}
}

// resetUserConnections has the nodes close the user's open connections, of all its keys or
// of one set (the user's own, a bound device's), on every node or on one. The access stays
// as it is, so the apps connect again at once: what to do with a device that hangs or keeps
// an old route. A node older than the call is skipped and named in the answer.
func (h *handlers) resetUserConnections(ctx context.Context, in *resetConnectionsInput) (*resetConnectionsOutput, error) {
	u, err := h.d.Users.Get(ctx, in.ID)
	if err != nil {
		return nil, mapDomainErr(err)
	}
	var slots []string
	switch dev := in.Body.DeviceID; {
	case dev == nil:
		if slots, err = h.d.Store.Q.ListUserSlots(ctx, in.ID); err != nil {
			return nil, err
		}
	case *dev == 0:
		if u.SlotID.Valid {
			s, err := h.d.Store.Q.GetSlot(ctx, u.SlotID.Int64)
			if err != nil {
				return nil, err
			}
			slots = []string{s.Name}
		}
	default:
		rows, err := h.d.Store.Q.ListBoundDeviceSlots(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			if r.DeviceID == *dev {
				slots = []string{r.SlotName}
			}
		}
		if slots == nil {
			return nil, huma.Error404NotFound("not_found")
		}
	}
	nodes, err := h.d.Store.Q.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	name := make(map[int64]string, len(nodes))
	for _, n := range nodes {
		name[n.ID] = n.Name
	}
	if _, ok := name[in.Body.NodeID]; in.Body.NodeID != 0 && !ok {
		return nil, huma.Error404NotFound("not_found")
	}
	out := &resetConnectionsOutput{}
	out.Body.Nodes = []ConnResetView{}
	var skipped []int64
	if h.d.ResetSlots != nil && len(slots) > 0 {
		for _, r := range h.d.ResetSlots(ctx, in.Body.NodeID, slots) {
			v := ConnResetView{NodeID: r.Node, NodeName: name[r.Node], Status: "ok", Closed: r.Closed}
			var se *nodeapi.StatusError
			switch {
			case errors.As(r.Err, &se) && se.Status == http.StatusNotFound:
				v.Status, v.Closed = "node_too_old", 0
			case r.Err != nil:
				h.d.Log.Warn("reset connections", "node", r.Node, "user", in.ID, "err", r.Err)
				v.Status, v.Closed = "node_unavailable", 0
			}
			if v.Status != "ok" {
				skipped = append(skipped, v.NodeID)
			}
			out.Body.Closed += v.Closed
			out.Body.Nodes = append(out.Body.Nodes, v)
		}
	}
	sort.Slice(out.Body.Nodes, func(i, j int) bool { return out.Body.Nodes[i].NodeID < out.Body.Nodes[j].NodeID })
	slices.Sort(skipped)
	details := map[string]any{"closed": out.Body.Closed}
	if in.Body.NodeID != 0 {
		details["node"] = in.Body.NodeID
	}
	if in.Body.DeviceID != nil {
		details["device"] = *in.Body.DeviceID
	}
	if skipped != nil {
		details["skipped"] = skipped
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "user.reset_connections", "user", strconv.FormatInt(in.ID, 10), details)
	return out, nil
}
