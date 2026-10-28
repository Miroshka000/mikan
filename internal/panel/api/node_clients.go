package api

import (
	"context"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

// Who goes through a node and which nodes a user goes through: user_node_traffic, the bytes
// of the users' statistics by user and node (nodesync counts them on the batch's
// transaction), with the last stored batch that had the user's bytes there. Online now comes
// from the nodes' last counters, as in userOnline. The names are user_name and the addresses
// online_ips: the people mask hides both from an operator who may not see the subscribers.

// NodeClientOnline is one set of a user's keys open on the node now.
type NodeClientOnline struct {
	DeviceID *int64   `json:"device_id" doc:"Привязанное устройство, чьими ключами подключены; null — общие ключи пользователя"`
	IPs      []string `json:"online_ips" doc:"Адреса с открытыми соединениями; оператор поддержки без доступа к подписчикам получает пустой список"`
	Conns    int      `json:"conns"`
}

// NodeClient is a user who went through the node.
type NodeClient struct {
	UserID   int64              `json:"user_id"`
	UserName string             `json:"user_name" doc:"Имя пользователя; оператор поддержки без доступа к подписчикам видит #ID"`
	Up       int64              `json:"up" doc:"Отдано через эту ноду с начала учёта"`
	Down     int64              `json:"down" doc:"Скачано через эту ноду с начала учёта"`
	LastSeen time.Time          `json:"last_seen" doc:"Последняя сохранённая порция трафика пользователя на этой ноде"`
	Online   []NodeClientOnline `json:"online" doc:"Что открыто на ноде сейчас, по ключам; пусто — не в сети"`
}

type nodeClientsInput struct {
	ID     int64  `path:"id" minimum:"1"`
	Query  string `query:"q" maxLength:"100" doc:"Часть имени пользователя. Оператору поддержки без показа подписчиков закрыт: 403 support_denied"`
	Limit  int32  `query:"limit" minimum:"1" maximum:"200" default:"50"`
	Offset int32  `query:"offset" minimum:"0" default:"0"`
}

type nodeClientsOutput struct {
	Body struct {
		Items  []NodeClient `json:"items" doc:"Последние увиденные сверху"`
		Total  int64        `json:"total" doc:"Сколько всего пользователей под запрос"`
		Online int          `json:"online" doc:"Сколько пользователей в сети на ноде сейчас, без учёта запроса"`
	}
}

// UserNode is a node the user went through.
type UserNode struct {
	NodeID   int64     `json:"node_id"`
	NodeName string    `json:"node_name" doc:"Имя ноды; у своей ноды панели может быть пустым"`
	Local    bool      `json:"local" doc:"Своя нода панели"`
	Up       int64     `json:"up" doc:"Отдано через эту ноду с начала учёта"`
	Down     int64     `json:"down" doc:"Скачано через эту ноду с начала учёта"`
	LastSeen time.Time `json:"last_seen" doc:"Последняя сохранённая порция трафика пользователя на этой ноде"`
	Online   bool      `json:"online" doc:"Подключён к этой ноде сейчас"`
}

type userNodesOutput struct{ Body []UserNode }

func (h *handlers) registerNodeClients() {
	huma.Register(h.api, huma.Operation{OperationID: "node-clients", Method: http.MethodGet, Path: "/api/v1/nodes/{id}/clients", Summary: "Клиенты ноды",
		Description: "Кто ходил через ноду: последний раз, трафик на ней и кто в сети сейчас. Учёт идёт с версии, где он появился.", Tags: []string{"node"}}, h.nodeClients)
	huma.Register(h.api, huma.Operation{OperationID: "user-nodes", Method: http.MethodGet, Path: "/api/v1/users/{id}/nodes", Summary: "Ноды пользователя",
		Description: "Через какие ноды ходил пользователь: последний раз и трафик на каждой. Учёт идёт с версии, где он появился.", Tags: []string{"users"}}, h.userNodes)
}

func (h *handlers) nodeClients(ctx context.Context, in *nodeClientsInput) (*nodeClientsOutput, error) {
	q := strings.TrimSpace(in.Query)
	if q != "" && hidesPeople(ctx) {
		return nil, huma.Error403Forbidden("support_denied")
	}
	if _, err := h.getNode(ctx, in.ID); err != nil {
		return nil, err
	}
	rows, err := h.d.Store.Q.ListNodeClients(ctx, db.ListNodeClientsParams{NodeID: in.ID, Q: q, Lim: in.Limit, Off: in.Offset})
	if err != nil {
		return nil, err
	}
	total, err := h.d.Store.Q.CountNodeClients(ctx, db.CountNodeClientsParams{NodeID: in.ID, Q: q})
	if err != nil {
		return nil, err
	}
	var online map[string]nodeapi.Online
	if h.d.NodeOnline != nil {
		online = h.d.NodeOnline()[in.ID]
	}
	out := &nodeClientsOutput{}
	out.Body.Items, out.Body.Total = make([]NodeClient, 0, len(rows)), total
	ids := make([]int64, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.UserID)
	}
	// The page's users' keys: what of them is open on the node now.
	open := map[int64][]NodeClientOnline{}
	if len(online) > 0 && len(ids) > 0 {
		slots, err := h.d.Store.Q.ListUsersSlotDevices(ctx, ids)
		if err != nil {
			return nil, err
		}
		for _, s := range slots {
			on, ok := online[s.SlotName]
			if !ok || (len(on.IPs) == 0 && on.Conns == 0) {
				continue
			}
			v := NodeClientOnline{IPs: slices.Sorted(slices.Values(on.IPs)), Conns: on.Conns}
			if v.IPs == nil {
				v.IPs = []string{}
			}
			if s.DeviceID != 0 {
				v.DeviceID = &s.DeviceID
			}
			open[s.UserID] = append(open[s.UserID], v)
		}
		// The user's own keys first, then the devices by id.
		for _, list := range open {
			sort.Slice(list, func(i, j int) bool {
				a, b := list[i].DeviceID, list[j].DeviceID
				if (a == nil) != (b == nil) {
					return a == nil
				}
				return a != nil && *a < *b
			})
		}
	}
	for _, r := range rows {
		c := NodeClient{UserID: r.UserID, UserName: r.Name, Up: r.Up, Down: r.Down, LastSeen: store.Time(r.LastSeen), Online: open[r.UserID]}
		if c.Online == nil {
			c.Online = []NodeClientOnline{}
		}
		out.Body.Items = append(out.Body.Items, c)
	}
	// Everyone online on the node now, whether on this page or not.
	if len(online) > 0 {
		names := make([]string, 0, len(online))
		for slot, on := range online {
			if len(on.IPs) > 0 || on.Conns > 0 {
				names = append(names, slot)
			}
		}
		owners, err := h.d.Store.Q.SlotOwners(ctx, names)
		if err != nil {
			return nil, err
		}
		users := map[int64]bool{}
		for _, o := range owners {
			users[o.UserID] = true
		}
		out.Body.Online = len(users)
	}
	return out, nil
}

func (h *handlers) userNodes(ctx context.Context, in *userIDInput) (*userNodesOutput, error) {
	if _, err := h.d.Users.Get(ctx, in.ID); err != nil {
		return nil, mapDomainErr(err)
	}
	rows, err := h.d.Store.Q.ListUserNodes(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	// The nodes where one of the user's keys is open now.
	here := map[int64]bool{}
	if h.d.NodeOnline != nil && len(rows) > 0 {
		slots, err := h.d.Store.Q.ListUserSlots(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		for id, online := range h.d.NodeOnline() {
			for _, s := range slots {
				if on, ok := online[s]; ok && (len(on.IPs) > 0 || on.Conns > 0) {
					here[id] = true
				}
			}
		}
	}
	out := &userNodesOutput{Body: make([]UserNode, 0, len(rows))}
	for _, r := range rows {
		out.Body = append(out.Body, UserNode{NodeID: r.NodeID, NodeName: r.Name, Local: r.Local, Up: r.Up, Down: r.Down,
			LastSeen: store.Time(r.LastSeen), Online: here[r.NodeID]})
	}
	return out, nil
}
