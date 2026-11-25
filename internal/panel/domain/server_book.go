package domain

import (
	"context"
	"database/sql"

	"mikan/internal/panel/store/db"
)

// ServerBook is what every client stands on, read at once (LoadServerBook): the servers of
// every tariff and every group that chose them, and the group of every client and tariff that
// has one. The nodes and the block detector tell every client's access from it, as UserServers
// tells one client's.
type ServerBook struct {
	tariffs     map[int64]Servers // the tariffs that chose servers (AllTariffServers)
	groups      map[int64]Servers // the groups that chose servers
	members     map[int64]int64   // the group of each client that has one of its own
	tariffGroup map[int64]int64   // the group of each tariff that has one
}

// LoadServerBook reads the book from q.
func LoadServerBook(ctx context.Context, q *db.Queries) (ServerBook, error) {
	b := ServerBook{groups: map[int64]Servers{}}
	var err error
	if b.tariffs, err = AllTariffServers(ctx, q); err != nil {
		return b, err
	}
	groups, err := q.ListGroups(ctx)
	if err != nil {
		return b, err
	}
	for _, g := range groups {
		if g.ChosenServers != 0 {
			b.groups[g.ID] = Servers{Nodes: map[int64]bool{}, Inbounds: map[int64]bool{}}
		}
	}
	if len(b.groups) > 0 {
		nodes, err := q.ListAllGroupNodes(ctx)
		if err != nil {
			return b, err
		}
		for _, n := range nodes {
			if s, ok := b.groups[n.GroupID]; ok {
				s.Nodes[n.NodeID] = true
			}
		}
		ins, err := q.ListAllGroupInbounds(ctx)
		if err != nil {
			return b, err
		}
		for _, in := range ins {
			if s, ok := b.groups[in.GroupID]; ok {
				s.Inbounds[in.InboundID] = true
			}
		}
	}
	if b.members, err = UserGroupIDs(ctx, q); err != nil {
		return b, err
	}
	b.tariffGroup, err = TariffGroupIDs(ctx, q)
	return b, err
}

// UserGroupIDs is the group of every client that has one of its own, by the client's id.
func UserGroupIDs(ctx context.Context, q *db.Queries) (map[int64]int64, error) {
	rows, err := q.ListUserGroupIDs(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]int64, len(rows))
	for _, r := range rows {
		out[r.UserID] = r.GroupID
	}
	return out, nil
}

// TariffGroupIDs is the group of every tariff that has one, by the tariff's id.
func TariffGroupIDs(ctx context.Context, q *db.Queries) (map[int64]int64, error) {
	rows, err := q.ListTariffGroupIDs(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]int64, len(rows))
	for _, r := range rows {
		out[r.TariffID] = r.GroupID
	}
	return out, nil
}

// GroupOf is the group u is in (GroupOf).
func (b ServerBook) GroupOf(u db.User) int64 {
	tariff := int64(0)
	if u.TariffID.Valid {
		tariff = b.tariffGroup[u.TariffID.Int64]
	}
	return GroupOf(b.members[u.ID], tariff)
}

// Of are the servers u stands on: its group's when the group chose them, else its tariff's.
func (b ServerBook) Of(u db.User) Servers {
	if s, ok := b.groups[b.GroupOf(u)]; ok {
		return s
	}
	return ServersOf(b.tariffs, u.TariffID)
}

// OfTariff are the servers a client of tariff id stands on without a group of its own
// (TariffClientServers).
func (b ServerBook) OfTariff(id int64) Servers {
	if s, ok := b.groups[b.tariffGroup[id]]; ok {
		return s
	}
	return ServersOf(b.tariffs, sql.NullInt64{Int64: id, Valid: true})
}
