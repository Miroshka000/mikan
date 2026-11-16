package domain

import (
	"context"
	"database/sql"
	"errors"

	"mikan/internal/panel/store/db"
)

// Servers is what a tariff includes: every server (All), or only the chosen ones: nodes
// whole, with the protocols added to them later, and single protocols.
type Servers struct {
	All      bool
	Nodes    map[int64]bool
	Inbounds map[int64]bool
}

// AllServers is a tariff that includes every server, and what a client without a tariff
// may use.
var AllServers = Servers{All: true}

// Has says whether the tariff includes inbound in.
func (s Servers) Has(in db.Inbound) bool { return s.All || s.Nodes[in.NodeID] || s.Inbounds[in.ID] }

// TariffServers are the servers of tariff id; no tariff includes every server. They are the
// tariff's own: what a client of the tariff stands on is TariffClientServers.
func TariffServers(ctx context.Context, q *db.Queries, id sql.NullInt64) (Servers, error) {
	t, ok, err := tariffOf(ctx, q, id)
	if err != nil || !ok {
		return AllServers, err
	}
	return tariffServers(ctx, q, t)
}

// tariffOf is tariff id; false when there is none (no id, or a tariff that is gone).
func tariffOf(ctx context.Context, q *db.Queries, id sql.NullInt64) (db.Tariff, bool, error) {
	if !id.Valid {
		return db.Tariff{}, false, nil
	}
	t, err := q.GetTariff(ctx, id.Int64)
	if errors.Is(err, sql.ErrNoRows) {
		return db.Tariff{}, false, nil
	}
	return t, err == nil, err
}

// tariffServers are the servers tariff t includes itself.
func tariffServers(ctx context.Context, q *db.Queries, t db.Tariff) (Servers, error) {
	if t.ChosenServers == 0 {
		return AllServers, nil
	}
	return chosenServers(func() ([]int64, error) { return q.ListTariffNodes(ctx, t.ID) }, func() ([]int64, error) { return q.ListTariffInbounds(ctx, t.ID) })
}

// chosenServers are the chosen servers nodes and inbounds list.
func chosenServers(nodes, inbounds func() ([]int64, error)) (Servers, error) {
	s := Servers{Nodes: map[int64]bool{}, Inbounds: map[int64]bool{}}
	ns, err := nodes()
	if err != nil {
		return s, err
	}
	for _, n := range ns {
		s.Nodes[n] = true
	}
	ins, err := inbounds()
	if err != nil {
		return s, err
	}
	for _, in := range ins {
		s.Inbounds[in] = true
	}
	return s, nil
}

// AllTariffServers are the servers of every tariff that includes only chosen ones, archived
// ones too (their subscribers keep them to the end of their term), by tariff id; a tariff
// missing from it includes every server (ServersOf).
func AllTariffServers(ctx context.Context, q *db.Queries) (map[int64]Servers, error) {
	ids, err := q.ListChosenServerTariffs(ctx)
	if err != nil {
		return nil, err
	}
	out := map[int64]Servers{}
	for _, id := range ids {
		out[id] = Servers{Nodes: map[int64]bool{}, Inbounds: map[int64]bool{}}
	}
	if len(out) == 0 {
		return out, nil
	}
	nodes, err := q.ListAllTariffNodes(ctx)
	if err != nil {
		return nil, err
	}
	for _, n := range nodes {
		if s, ok := out[n.TariffID]; ok {
			s.Nodes[n.NodeID] = true
		}
	}
	ins, err := q.ListAllTariffInbounds(ctx)
	if err != nil {
		return nil, err
	}
	for _, in := range ins {
		if s, ok := out[in.TariffID]; ok {
			s.Inbounds[in.InboundID] = true
		}
	}
	return out, nil
}

// ServersOf is the servers of tariff id in m (AllTariffServers).
func ServersOf(m map[int64]Servers, id sql.NullInt64) Servers {
	if s, ok := m[id.Int64]; ok && id.Valid {
		return s
	}
	return AllServers
}

// ServersInput is what SetTariffServers writes.
type ServersInput struct {
	All      bool
	Nodes    []int64
	Inbounds []int64
}

// SetTariffServers makes in the servers of tariff id on q's transaction. Chosen servers
// name at least one node or protocol, each of them existing.
func SetTariffServers(ctx context.Context, q *db.Queries, id int64, in ServersInput) error {
	return writeServers(ctx, q, in, serverChoice{
		clear: func() error {
			if err := q.ClearTariffNodes(ctx, id); err != nil {
				return err
			}
			return q.ClearTariffInbounds(ctx, id)
		},
		node: func(n int64) error { return q.AddTariffNode(ctx, db.AddTariffNodeParams{TariffID: id, NodeID: n}) },
		inbound: func(i int64) error {
			return q.AddTariffInbound(ctx, db.AddTariffInboundParams{TariffID: id, InboundID: i})
		},
		chosen: func(on bool) error {
			return q.SetTariffChosenServers(ctx, db.SetTariffChosenServersParams{ID: id, ChosenServers: Flag(on)})
		},
	})
}

// serverChoice is where a choice of servers is written: a tariff's rows or a group's.
type serverChoice struct {
	clear   func() error
	node    func(id int64) error
	inbound func(id int64) error
	chosen  func(on bool) error
}

// writeServers makes in the servers of w on q's transaction. Chosen servers name at least one
// node or protocol, each of them existing.
func writeServers(ctx context.Context, q *db.Queries, in ServersInput, w serverChoice) error {
	if err := w.clear(); err != nil {
		return err
	}
	if in.All {
		return w.chosen(false)
	}
	if len(in.Nodes) == 0 && len(in.Inbounds) == 0 {
		return fieldErr("servers", "servers_empty")
	}
	for _, n := range in.Nodes {
		if _, err := q.GetNode(ctx, n); errors.Is(err, sql.ErrNoRows) {
			return fieldErr("servers.nodes", "node_not_found")
		} else if err != nil {
			return err
		}
		if err := w.node(n); err != nil {
			return err
		}
	}
	for _, i := range in.Inbounds {
		if _, err := q.GetInbound(ctx, i); errors.Is(err, sql.ErrNoRows) {
			return fieldErr("servers.inbounds", "inbound_not_found")
		} else if err != nil {
			return err
		}
		if err := w.inbound(i); err != nil {
			return err
		}
	}
	return w.chosen(true)
}

// Access is what a client may connect to: the protocols the client's card lists, when it
// lists any, over the servers the client stands on: its group's or its tariff's (UserServers).
type Access struct {
	own     map[int64]bool
	servers Servers
}

// AccessOf is u's access with s, the servers u stands on (UserServers, ServerBook.Of).
func AccessOf(u db.User, s Servers) Access {
	a := Access{servers: s}
	if ids := DecodeInbounds(u.Inbounds); len(ids) > 0 {
		a.own = make(map[int64]bool, len(ids))
		for _, id := range ids {
			a.own[id] = true
		}
	}
	return a
}

// Allows says whether the client may connect to in.
func (a Access) Allows(in db.Inbound) bool {
	if a.own != nil {
		return a.own[in.ID]
	}
	return a.servers.Has(in)
}

// Usable says whether the client may connect to in now: it is on and the access allows it.
func (a Access) Usable(in db.Inbound) bool { return in.Enabled != 0 && a.Allows(in) }

// Own says whether the protocols the client's card lists decide, not the servers it stands on.
func (a Access) Own() bool { return a.own != nil }

// Groups are the server groups the access reaches: those with a protocol of ins the client
// may connect to now. A package or a gift of a group outside them is traffic the client has
// nothing to spend on, and a group's page shows nothing of one it cannot reach.
func (a Access) Groups(ins []db.Inbound) map[int64]bool {
	out := map[int64]bool{}
	for _, in := range ins {
		if in.PoolID.Valid && a.Usable(in) {
			out[in.PoolID.Int64] = true
		}
	}
	return out
}

// Groups are the server groups the servers serve: those with a protocol of ins that is on
// and among them. What a tariff can give a package or a gift of.
func (s Servers) Groups(ins []db.Inbound) map[int64]bool {
	return Access{servers: s}.Groups(ins)
}

// UserGroups are the server groups u reaches (Access.Groups), read from q.
func UserGroups(ctx context.Context, q *db.Queries, u db.User) (map[int64]bool, error) {
	stand, err := UserServers(ctx, q, u)
	if err != nil {
		return nil, err
	}
	ins, err := q.ListInbounds(ctx)
	if err != nil {
		return nil, err
	}
	return AccessOf(u, stand.Servers).Groups(ins), nil
}

// TariffGroups are the server groups served by the servers a client of tariff t stands on
// (TariffClientServers, Servers.Groups).
func TariffGroups(ctx context.Context, q *db.Queries, t db.Tariff) (map[int64]bool, error) {
	servers, err := TariffClientServers(ctx, q, t)
	if err != nil {
		return nil, err
	}
	ins, err := q.ListInbounds(ctx)
	if err != nil {
		return nil, err
	}
	return servers.Groups(ins), nil
}

// Everything says whether the client may connect to every inbound.
func (a Access) Everything() bool { return a.own == nil && a.servers.All }

// IDs are the ids of the inbounds of ins the client may connect to; nil when every one.
func (a Access) IDs(ins []db.Inbound) []int64 {
	if a.Everything() {
		return nil
	}
	out := []int64{}
	for _, in := range ins {
		if a.Allows(in) {
			out = append(out, in.ID)
		}
	}
	return out
}
