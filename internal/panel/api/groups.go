package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

// Groups of clients (#290): what a group sees, and later how its subscriptions look. A client
// is in one group at most, its own or its tariff's (domain.GroupOf). Unlike a folder, a group
// decides what its clients may connect to (domain.UserServers): every change of one is told to
// the nodes.

// maxGroups keeps the groups a list an admin can still read.
const maxGroups = 100

// GroupServers are the servers a group sees: its clients' tariffs', or the chosen ones.
type GroupServers struct {
	AsTariff bool    `json:"as_tariff" doc:"Серверы как в тарифе клиента; false — только выбранные ноды и протоколы, вместо тарифных. Свой список протоколов в карточке клиента сильнее и того и другого"`
	Nodes    []int64 `json:"nodes" doc:"Ноды целиком, с протоколами, которые на них появятся потом"`
	Inbounds []int64 `json:"inbounds" doc:"Отдельные протоколы"`
}

// groupServersBody is a group's servers as a body sets them.
type groupServersBody struct {
	AsTariff bool    `json:"as_tariff" doc:"Серверы как в тарифе клиента; false — только выбранные, хотя бы одна нода или протокол"`
	Nodes    []int64 `json:"nodes,omitempty" maxItems:"500" doc:"Ноды целиком, с протоколами, которые на них появятся потом"`
	Inbounds []int64 `json:"inbounds,omitempty" maxItems:"2000" doc:"Отдельные протоколы"`
}

// GroupHostView is an address for clients that names the group in whom it is shown to.
type GroupHostView struct {
	ID          int64  `json:"id"`
	InboundID   int64  `json:"inbound_id"`
	NodeID      int64  `json:"node_id"`
	InboundName string `json:"inbound_name"`
	Default     bool   `json:"default" doc:"Адрес по умолчанию: из настроек подключения"`
	Address     string `json:"address" doc:"Пусто — адрес ноды"`
	Name        string `json:"name" doc:"Своё имя адреса в подписке; пусто — имя подключения"`
	Mode        string `json:"mode" enum:"only,never" doc:"only — адрес выдаётся только клиентам названных групп, never — всем, кроме них"`
}

type GroupView struct {
	ID       int64           `json:"id"`
	Name     string          `json:"name"`
	Color    string          `json:"color" enum:"gray,red,orange,yellow,green,teal,blue,purple,pink"`
	Emoji    string          `json:"emoji" doc:"Значок перед названием; может быть пустым"`
	Users    int             `json:"users" doc:"Сколько клиентов в группе: своей группой и через тариф, скрытые тоже"`
	OwnUsers int             `json:"own_users" doc:"Из них в группе своей группой, заданной в карточке"`
	Tariffs  int             `json:"tariffs" doc:"Сколько тарифов, кроме архивных, кладут своих клиентов в эту группу"`
	Servers  GroupServers    `json:"servers"`
	Hosts    []GroupHostView `json:"hosts" doc:"Адреса для клиентов, которые выдаются по группам и называют эту"`
}

type groupsOutput struct{ Body []GroupView }
type groupOutput struct{ Body GroupView }

type groupIDInput struct {
	ID int64 `path:"id" minimum:"1"`
}

type createGroupInput struct {
	Body struct {
		Name    string            `json:"name" minLength:"1" maxLength:"40"`
		Color   string            `json:"color,omitempty" enum:"gray,red,orange,yellow,green,teal,blue,purple,pink" doc:"Не задан — gray"`
		Emoji   string            `json:"emoji,omitempty" maxLength:"16"`
		Servers *groupServersBody `json:"servers,omitempty" doc:"Не передан — как в тарифе"`
	}
}

type patchGroupInput struct {
	ID   int64 `path:"id" minimum:"1"`
	Body struct {
		Name    *string           `json:"name,omitempty" minLength:"1" maxLength:"40"`
		Color   *string           `json:"color,omitempty" enum:"gray,red,orange,yellow,green,teal,blue,purple,pink"`
		Emoji   *string           `json:"emoji,omitempty" maxLength:"16" doc:"Пустая строка убирает значок"`
		Servers *groupServersBody `json:"servers,omitempty" doc:"Не передан — без изменений. Действует сразу на всех клиентов группы"`
	}
}

type orderGroupsInput struct {
	Body struct {
		IDs []int64 `json:"ids" minItems:"1" maxItems:"1000" doc:"Все группы, каждая один раз, в том порядке, в каком их показывать"`
	}
}

func (h *handlers) registerGroups() {
	tags := []string{"users"}
	huma.Register(h.api, huma.Operation{OperationID: "list-groups", Method: http.MethodGet, Path: "/api/v1/groups", Summary: "Группы клиентов", Tags: tags}, h.listGroups)
	huma.Register(h.api, huma.Operation{OperationID: "create-group", Method: http.MethodPost, Path: "/api/v1/groups", Summary: "Создать группу клиентов", Tags: tags, DefaultStatus: http.StatusCreated}, h.createGroup)
	huma.Register(h.api, huma.Operation{OperationID: "update-group", Method: http.MethodPatch, Path: "/api/v1/groups/{id}", Summary: "Изменить группу клиентов", Tags: tags}, h.updateGroup)
	huma.Register(h.api, huma.Operation{OperationID: "order-groups", Method: http.MethodPut, Path: "/api/v1/groups/order", Summary: "Порядок групп клиентов", Tags: tags, DefaultStatus: http.StatusNoContent}, h.orderGroups)
	huma.Register(h.api, huma.Operation{OperationID: "delete-group", Method: http.MethodDelete, Path: "/api/v1/groups/{id}", Summary: "Удалить группу: её клиенты и тарифы остаются без неё",
		Description: "Клиенты группы возвращаются к серверам тарифа. Адрес, который выдавался только этой группе, больше не выдаётся никому, пока ему не выберут другие группы.", Tags: tags, DefaultStatus: http.StatusNoContent}, h.deleteGroup)
}

// groupExtras is what the groups' views take besides their rows, read once for all of them.
type groupExtras struct {
	users   map[int64]db.CountGroupUsersRow
	tariffs map[int64]int64
	servers map[int64]GroupServers
	hosts   map[int64][]GroupHostView
}

func (h *handlers) groupExtras(ctx context.Context, q *db.Queries) (groupExtras, error) {
	e := groupExtras{users: map[int64]db.CountGroupUsersRow{}, tariffs: map[int64]int64{}, servers: map[int64]GroupServers{}, hosts: map[int64][]GroupHostView{}}
	users, err := q.CountGroupUsers(ctx)
	if err != nil {
		return e, err
	}
	for _, r := range users {
		e.users[r.GroupID] = r
	}
	tariffs, err := q.CountGroupTariffs(ctx)
	if err != nil {
		return e, err
	}
	for _, r := range tariffs {
		e.tariffs[r.GroupID] = r.N
	}
	nodes, err := q.ListAllGroupNodes(ctx)
	if err != nil {
		return e, err
	}
	for _, n := range nodes {
		s := e.servers[n.GroupID]
		s.Nodes = append(s.Nodes, n.NodeID)
		e.servers[n.GroupID] = s
	}
	ins, err := q.ListAllGroupInbounds(ctx)
	if err != nil {
		return e, err
	}
	for _, in := range ins {
		s := e.servers[in.GroupID]
		s.Inbounds = append(s.Inbounds, in.InboundID)
		e.servers[in.GroupID] = s
	}
	hosts, err := q.ListGroupedHosts(ctx)
	if err != nil {
		return e, err
	}
	for _, r := range hosts {
		v := GroupHostView{ID: r.ID, InboundID: r.InboundID, NodeID: r.NodeID, InboundName: r.InboundName, Default: r.Position == 0, Address: r.Address, Name: r.Name, Mode: r.GroupMode}
		for _, g := range r.Groups {
			e.hosts[g] = append(e.hosts[g], v)
		}
	}
	return e, nil
}

func (e groupExtras) view(g db.UserGroup) GroupView {
	u := e.users[g.ID]
	v := GroupView{ID: g.ID, Name: g.Name, Color: g.Color, Emoji: g.Emoji, Users: int(u.N), OwnUsers: int(u.Own), Tariffs: int(e.tariffs[g.ID]), Hosts: e.hosts[g.ID]}
	s := e.servers[g.ID]
	v.Servers = GroupServers{AsTariff: g.ChosenServers == 0, Nodes: s.Nodes, Inbounds: s.Inbounds}
	if g.ChosenServers == 0 || v.Servers.Nodes == nil {
		v.Servers.Nodes = []int64{}
	}
	if g.ChosenServers == 0 || v.Servers.Inbounds == nil {
		v.Servers.Inbounds = []int64{}
	}
	if v.Hosts == nil {
		v.Hosts = []GroupHostView{}
	}
	return v
}

func (h *handlers) listGroups(ctx context.Context, _ *struct{}) (*groupsOutput, error) {
	gs, err := h.d.Store.Q.ListGroups(ctx)
	if err != nil {
		return nil, err
	}
	e, err := h.groupExtras(ctx, h.d.Store.Q)
	if err != nil {
		return nil, err
	}
	out := &groupsOutput{Body: make([]GroupView, 0, len(gs))}
	for _, g := range gs {
		out.Body = append(out.Body, e.view(g))
	}
	return out, nil
}

// groupOut is the view of group id as it is now.
func (h *handlers) groupOut(ctx context.Context, id int64) (*groupOutput, error) {
	g, err := h.d.Store.Q.GetGroup(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, huma.Error404NotFound("not_found")
	}
	if err != nil {
		return nil, err
	}
	e, err := h.groupExtras(ctx, h.d.Store.Q)
	if err != nil {
		return nil, err
	}
	return &groupOutput{Body: e.view(g)}, nil
}

// groupNameTaken: two groups may not differ by case alone, which the database's unique index
// does not see; self is the group being renamed.
func groupNameTaken(all []db.UserGroup, name string, self int64) bool {
	return slices.ContainsFunc(all, func(g db.UserGroup) bool { return g.ID != self && strings.EqualFold(g.Name, name) })
}

func groupNameTakenError() error {
	return huma.Error409Conflict("group_name_taken", &huma.ErrorDetail{Location: "body.name", Message: "group_name_taken"})
}

// setGroupServers writes the servers b sets for group id on q's transaction.
func setGroupServers(ctx context.Context, q *db.Queries, id int64, b *groupServersBody) error {
	if b == nil {
		return nil
	}
	err := domain.SetGroupServers(ctx, q, id, domain.ServersInput{All: b.AsTariff, Nodes: b.Nodes, Inbounds: b.Inbounds})
	return mapDomainErr(err)
}

func (h *handlers) createGroup(ctx context.Context, in *createGroupInput) (*groupOutput, error) {
	name, err := folderName(in.Body.Name)
	if err != nil {
		return nil, err
	}
	color, err := folderColor(in.Body.Color)
	if err != nil {
		return nil, err
	}
	var g db.UserGroup
	err = h.d.Store.Tx(ctx, func(q *db.Queries) error {
		all, err := q.ListGroups(ctx)
		if err != nil {
			return err
		}
		if len(all) >= maxGroups {
			return huma.Error409Conflict("group_limit")
		}
		if groupNameTaken(all, name, 0) {
			return groupNameTakenError()
		}
		if g, err = q.CreateGroup(ctx, db.CreateGroupParams{Name: name, Color: color, Emoji: strings.TrimSpace(in.Body.Emoji), CreatedAt: h.d.Now().Unix()}); err != nil {
			return err
		}
		return setGroupServers(ctx, q, g.ID, in.Body.Servers)
	})
	if store.IsUnique(err) {
		err = groupNameTakenError()
	}
	if err != nil {
		return nil, err
	}
	// A new group has no clients yet: the nodes have nothing to learn of it.
	h.audit(ctx, sessionOf(ctx).AdminID, "group.create", "group", strconv.FormatInt(g.ID, 10), map[string]any{"name": name})
	return h.groupOut(ctx, g.ID)
}

func (h *handlers) updateGroup(ctx context.Context, in *patchGroupInput) (*groupOutput, error) {
	err := h.d.Store.Tx(ctx, func(q *db.Queries) error {
		g, err := q.GetGroup(ctx, in.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return huma.Error404NotFound("not_found")
		} else if err != nil {
			return err
		}
		b := in.Body
		if b.Name != nil {
			if g.Name, err = folderName(*b.Name); err != nil {
				return err
			}
			all, err := q.ListGroups(ctx)
			if err != nil {
				return err
			}
			if groupNameTaken(all, g.Name, g.ID) {
				return groupNameTakenError()
			}
		}
		if b.Color != nil {
			if g.Color, err = folderColor(*b.Color); err != nil {
				return err
			}
		}
		if b.Emoji != nil {
			g.Emoji = strings.TrimSpace(*b.Emoji)
		}
		if _, err = q.UpdateGroup(ctx, db.UpdateGroupParams{Name: g.Name, Color: g.Color, Emoji: g.Emoji, ID: g.ID}); err != nil {
			return err
		}
		return setGroupServers(ctx, q, g.ID, b.Servers)
	})
	if store.IsUnique(err) {
		err = groupNameTakenError()
	}
	if err != nil {
		return nil, err
	}
	if in.Body.Servers != nil {
		h.d.Changes.PoliciesChanged() // the group's clients see other servers
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "group.update", "group", strconv.FormatInt(in.ID, 10), map[string]any{"servers": in.Body.Servers != nil})
	return h.groupOut(ctx, in.ID)
}

// orderGroups sets the order the groups are shown in: the list must be every group once, so
// a stale page of the admin cannot silently drop or duplicate one.
func (h *handlers) orderGroups(ctx context.Context, in *orderGroupsInput) (*struct{}, error) {
	ids := in.Body.IDs
	err := h.d.Store.Tx(ctx, func(q *db.Queries) error {
		all, err := q.ListGroups(ctx)
		if err != nil {
			return err
		}
		have := make([]int64, len(all))
		for i, g := range all {
			have[i] = g.ID
		}
		return setOrder(ids, have, "group_order_mismatch", func(id, place int64) error {
			return q.SetGroupSort(ctx, db.SetGroupSortParams{Sort: place, ID: id})
		})
	})
	if err != nil {
		return nil, err
	}
	h.audit(ctx, sessionOf(ctx).AdminID, "group.order", "group", "", map[string]any{"ids": ids})
	return nil, nil
}

// deleteGroup lets the group's clients and tariffs go and takes it out of the addresses that
// name it. Its row first, then the addresses' and its links': every writer that gives a group
// takes its row before theirs (domain.LockGroups).
func (h *handlers) deleteGroup(ctx context.Context, in *groupIDInput) (*struct{}, error) {
	var freed int64
	var hosts int64
	err := h.d.Store.Tx(ctx, func(q *db.Queries) error {
		if _, err := q.LockGroupForDelete(ctx, in.ID); errors.Is(err, sql.ErrNoRows) {
			return huma.Error404NotFound("not_found")
		} else if err != nil {
			return err
		}
		// The clients are counted for the journal; the foreign keys let them go.
		counts, err := q.CountGroupUsers(ctx)
		if err != nil {
			return err
		}
		for _, r := range counts {
			if r.GroupID == in.ID {
				freed = r.N
			}
		}
		if hosts, err = q.RemoveGroupFromHosts(ctx, in.ID); err != nil {
			return err
		}
		_, err = q.DeleteGroup(ctx, in.ID)
		return err
	})
	if err != nil {
		return nil, err
	}
	h.d.Changes.PoliciesChanged() // its clients are back on their tariffs' servers
	h.audit(ctx, sessionOf(ctx).AdminID, "group.delete", "group", strconv.FormatInt(in.ID, 10), map[string]any{"users": freed, "hosts": hosts})
	return nil, nil
}
