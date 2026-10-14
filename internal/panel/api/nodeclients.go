package api

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/store/db"
)

// Who went through which node: the per-node half of the traffic, by user. Both cards read
// the same rows — a node's clients and a user's nodes are the two sides of one table.

// NodeClientView is one user on a node: what they carried over it and when they were last
// seen there.
type NodeClientView struct {
	UserID    int64      `json:"user_id"`
	Name      string     `json:"name"`
	Up        int64      `json:"up"`
	Down      int64      `json:"down"`
	Bytes     int64      `json:"bytes" doc:"up + down"`
	Online    bool       `json:"online" doc:"Подключён к этой ноде прямо сейчас"`
	FirstSeen *time.Time `json:"first_seen" doc:"Когда пользователя впервые увидели на ноде; null — время неизвестно"`
	LastSeen  *time.Time `json:"last_seen" doc:"Когда его в последний раз видели на ноде; null — время неизвестно"`
}

type nodeClientsOutput struct {
	Body struct {
		Items []NodeClientView `json:"items" doc:"Клиенты ноды, больше всех трафика сверху"`
		Total int64            `json:"total" doc:"Сколько всего клиентов у ноды; items может быть короче"`
	}
}

type nodeClientsInput struct {
	ID    int64 `path:"id" minimum:"1"`
	Limit int   `query:"limit" minimum:"1" maximum:"1000" default:"500" doc:"Сколько показать; всего клиентов в total"`
}

// UserNodeView is one node of a user: what they carried over it and when they were last
// seen there.
type UserNodeView struct {
	NodeID     int64      `json:"node_id"`
	Name       string     `json:"name"`
	PublicName string     `json:"public_name" doc:"Имя ноды в подписке; пусто — берётся name"`
	Up         int64      `json:"up"`
	Down       int64      `json:"down"`
	Bytes      int64      `json:"bytes" doc:"up + down"`
	Online     bool       `json:"online" doc:"Пользователь подключён к этой ноде прямо сейчас"`
	FirstSeen  *time.Time `json:"first_seen" doc:"Когда пользователя впервые увидели на ноде; null — время неизвестно"`
	LastSeen   *time.Time `json:"last_seen" doc:"Когда его в последний раз видели на ноде; null — время неизвестно"`
}

type userNodesOutput struct {
	Body struct {
		Items []UserNodeView `json:"items" doc:"Ноды пользователя, больше всех трафика сверху"`
	}
}

func (h *handlers) registerNodeClients() {
	huma.Register(h.api, huma.Operation{OperationID: "node-clients", Method: http.MethodGet, Path: "/api/v1/nodes/{id}/clients", Summary: "Клиенты ноды",
		Description: "Кто ходил через ноду, когда его видели в последний раз и сколько он через неё унёс. Считается с версии, где это появилось: прошлое не восстанавливается.",
		Tags:        []string{"node"}}, h.nodeClients)
	huma.Register(h.api, huma.Operation{OperationID: "user-nodes", Method: http.MethodGet, Path: "/api/v1/users/{id}/nodes", Summary: "Ноды пользователя",
		Description: "Через какие ноды ходил пользователь, сколько унёс через каждую и когда его там видели в последний раз.",
		Tags:        []string{"users"}}, h.userNodes)
}

// nodeOnline is one node's live view by slot name: empty without nodes or for one that is
// not running.
func (h *handlers) nodeOnline(id int64) map[string]nodeapi.Online {
	if h.d.NodeOnline == nil {
		return nil
	}
	return h.d.NodeOnline(id)
}

// onlineUsers is who of a node's slots is live right now, by user id.
func (h *handlers) onlineUsers(ctx context.Context, id int64) (map[int64]bool, error) {
	online := h.nodeOnline(id)
	if len(online) == 0 {
		return nil, nil
	}
	slots := make([]string, 0, len(online))
	for s := range online {
		slots = append(slots, s)
	}
	rows, err := h.d.Store.Q.SlotOwners(ctx, slots)
	if err != nil {
		return nil, err
	}
	live := make(map[int64]bool, len(rows))
	for _, r := range rows {
		live[r.UserID] = true
	}
	return live, nil
}

// ptrUnix is a unix time; the zero (or negative) is no time known, not 1970.
func ptrUnix(v int64) *time.Time {
	if v <= 0 {
		return nil
	}
	t := time.Unix(v, 0).UTC()
	return &t
}

func (h *handlers) nodeClients(ctx context.Context, in *nodeClientsInput) (*nodeClientsOutput, error) {
	if _, err := h.getNode(ctx, in.ID); err != nil {
		return nil, err
	}
	out := &nodeClientsOutput{}
	out.Body.Items = []NodeClientView{}
	rows, err := h.d.Store.Q.ListNodeClients(ctx, db.ListNodeClientsParams{NodeID: in.ID, Lim: int32(in.Limit)})
	if err != nil {
		return nil, err
	}
	if out.Body.Total, err = h.d.Store.Q.CountNodeClients(ctx, in.ID); err != nil {
		return nil, err
	}
	live, err := h.onlineUsers(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out.Body.Items = append(out.Body.Items, NodeClientView{
			UserID: r.UserID, Name: r.Name, Up: r.Up, Down: r.Down, Bytes: r.Up + r.Down,
			Online: live[r.UserID], FirstSeen: ptrUnix(r.FirstSeen), LastSeen: ptrUnix(r.LastSeen),
		})
	}
	return out, nil
}

func (h *handlers) userNodes(ctx context.Context, in *userIDInput) (*userNodesOutput, error) {
	if _, err := h.d.Users.Get(ctx, in.ID); err != nil {
		return nil, mapDomainErr(err)
	}
	out := &userNodesOutput{}
	out.Body.Items = []UserNodeView{}
	rows, err := h.d.Store.Q.ListUserNodes(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return out, nil
	}
	// A user's slots are the same on every node; each node's live view says where the user
	// is connected now.
	slots, err := h.d.Store.Q.ListUserSlots(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		live := false
		if on := h.nodeOnline(r.NodeID); len(on) > 0 {
			for _, s := range slots {
				if _, ok := on[s]; ok {
					live = true
					break
				}
			}
		}
		out.Body.Items = append(out.Body.Items, UserNodeView{
			NodeID: r.NodeID, Name: r.Name, PublicName: r.PublicName, Up: r.Up, Down: r.Down, Bytes: r.Up + r.Down,
			Online: live, FirstSeen: ptrUnix(r.FirstSeen), LastSeen: ptrUnix(r.LastSeen),
		})
	}
	return out, nil
}
