package api

import (
	"context"
	"net/http"
	"slices"
	"sort"

	"github.com/danielgtaylor/huma/v2"
)

// UserOnlineView is where a user is online now: one node and one set of keys (the user's own,
// or a bound device's), as that node last reported it.
type UserOnlineView struct {
	NodeID   int64  `json:"node_id"`
	NodeName string `json:"node_name"`
	// DeviceID is the bound device whose keys are online; nil: the user's own keys (apps
	// without a device id, or the panel binds no devices).
	DeviceID *int64   `json:"device_id" doc:"Привязанное устройство, чьими ключами подключены; null — общие ключи пользователя (приложения без ID или без привязки)"`
	IPs      []string `json:"ips" doc:"Адреса с открытыми соединениями на этой ноде"`
	Conns    int      `json:"conns" doc:"Открытые соединения на этой ноде"`
}

type userOnlineOutput struct{ Body []UserOnlineView }

func (h *handlers) registerUserOnline(tags []string) {
	huma.Register(h.api, huma.Operation{OperationID: "user-online", Method: http.MethodGet, Path: "/api/v1/users/{id}/online",
		Summary: "Где пользователь онлайн сейчас: ноды, устройства, адреса", Tags: tags}, h.userOnline)
}

// userOnline splits what the user list merges (online, online_ips) by node and device,
// from the nodes' last counters (every few seconds). It only shows: the nodes close a
// user's connections only when the user's keys stop being allowed (unbind, ban, expiry).
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
