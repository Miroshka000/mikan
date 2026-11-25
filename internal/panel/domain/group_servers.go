package domain

import (
	"context"
	"database/sql"
	"errors"

	"mikan/internal/panel/store/db"
)

// The access of a group of clients (#290). A client is in one group at most: its own
// (user_group_members), or else its tariff's (user_group_tariffs). A group leaves the servers
// to the tariff, or chooses its own as a tariff does (Servers). What a client may connect to
// comes, in this order, of the protocols its card lists (Access), of its group's servers when
// the group chose them, of its tariff's, and with none of these of every server.

// GroupOf is the group a client is in: own, its own (0: none), else tariffGroup, its tariff's;
// 0: none.
func GroupOf(own, tariffGroup int64) int64 {
	if own != 0 {
		return own
	}
	return tariffGroup
}

// OwnGroup is the group user id is in by its own card; 0: none.
func OwnGroup(ctx context.Context, q *db.Queries, id int64) (int64, error) {
	g, err := q.GetUserGroup(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return g, err
}

// TariffGroup is the group of the clients of tariff id that have none of their own; 0: none.
func TariffGroup(ctx context.Context, q *db.Queries, id sql.NullInt64) (int64, error) {
	if !id.Valid {
		return 0, nil
	}
	g, err := q.GetTariffGroup(ctx, id.Int64)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return g, err
}

// UserGroup is the group u is in (GroupOf), read from q.
func UserGroup(ctx context.Context, q *db.Queries, u db.User) (int64, error) {
	own, err := OwnGroup(ctx, q, u.ID)
	if err != nil || own != 0 {
		return own, err
	}
	return TariffGroup(ctx, q, u.TariffID)
}

// GroupServers are the servers group id chose; false when it leaves them to the tariff or is
// gone.
func GroupServers(ctx context.Context, q *db.Queries, id int64) (Servers, bool, error) {
	g, err := q.GetGroup(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Servers{}, false, nil
	}
	if err != nil || g.ChosenServers == 0 {
		return Servers{}, false, err
	}
	s, err := chosenServers(func() ([]int64, error) { return q.ListGroupNodes(ctx, id) }, func() ([]int64, error) { return q.ListGroupInbounds(ctx, id) })
	return s, err == nil, err
}

// SetGroupServers makes in the servers of group id on q's transaction: All leaves them to the
// tariff. Chosen servers name at least one node or protocol, each of them existing.
func SetGroupServers(ctx context.Context, q *db.Queries, id int64, in ServersInput) error {
	return writeServers(ctx, q, in, serverChoice{
		clear: func() error {
			if err := q.ClearGroupNodes(ctx, id); err != nil {
				return err
			}
			return q.ClearGroupInbounds(ctx, id)
		},
		node: func(n int64) error { return q.AddGroupNode(ctx, db.AddGroupNodeParams{GroupID: id, NodeID: n}) },
		inbound: func(i int64) error {
			return q.AddGroupInbound(ctx, db.AddGroupInboundParams{GroupID: id, InboundID: i})
		},
		chosen: func(on bool) error {
			return q.SetGroupChosenServers(ctx, db.SetGroupChosenServersParams{ID: id, ChosenServers: Flag(on)})
		},
	})
}

// Stand is what a user stands on: the servers AccessOf takes, the group it is in (GroupOf; 0:
// none), and whether the servers are the group's own (else its tariff's, or every server).
type Stand struct {
	Servers Servers
	Group   int64
	ByGroup bool
}

// UserServers is what u stands on: its group's servers when the group chose them, else its
// tariff's.
func UserServers(ctx context.Context, q *db.Queries, u db.User) (Stand, error) {
	group, err := UserGroup(ctx, q, u)
	if err != nil {
		return Stand{Servers: AllServers}, err
	}
	if group != 0 {
		if s, chosen, err := GroupServers(ctx, q, group); err != nil || chosen {
			return Stand{Servers: s, Group: group, ByGroup: chosen}, err
		}
	}
	s, err := TariffServers(ctx, q, u.TariffID)
	return Stand{Servers: s, Group: group}, err
}

// TariffClientServers are the servers a client of tariff t stands on without a group of its
// own: the tariff's group's when the group chose them, else the tariff's.
func TariffClientServers(ctx context.Context, q *db.Queries, t db.Tariff) (Servers, error) {
	group, err := TariffGroup(ctx, q, sql.NullInt64{Int64: t.ID, Valid: true})
	if err != nil {
		return AllServers, err
	}
	if group != 0 {
		if s, chosen, err := GroupServers(ctx, q, group); err != nil || chosen {
			return s, err
		}
	}
	return tariffServers(ctx, q, t)
}

// SetTariffGroup gives the clients of tariff id without a group of their own group (0: none)
// on q's transaction, the group locked first (LockGroups).
func SetTariffGroup(ctx context.Context, q *db.Queries, id, group int64) error {
	if group == 0 {
		return q.ClearTariffGroup(ctx, id)
	}
	return q.SetTariffGroup(ctx, db.SetTariffGroupParams{TariffID: id, GroupID: group})
}

// LockGroups keeps groups ids from being deleted until q's transaction ends: what is given one
// of them cannot meet a group that is gone. It is taken first in a transaction, as a group's
// deletion takes the group's row before the rows of what names it. A group that does not
// exist is refused as field's group_not_found.
func LockGroups(ctx context.Context, q *db.Queries, field string, ids ...int64) error {
	if len(ids) == 0 {
		return nil
	}
	got, err := q.LockGroups(ctx, ids)
	if err != nil {
		return err
	}
	for _, id := range ids {
		found := false
		for _, g := range got {
			found = found || g == id
		}
		if !found {
			return fieldErr(field, "group_not_found")
		}
	}
	return nil
}
