package domain

import (
	"context"
	"database/sql"
	"log/slog"
	"slices"
	"sort"
	"sync"
	"time"

	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

// Load balancing between nodes. Nodes that share a balance group share its clients out:
// each client is kept on one node of the group, the one with the most room when the client
// was first placed, and gets only that node's inbounds of the group in its subscription. A
// node's capacity caps the clients it takes; when every node of a group is full a new client
// goes to the least loaded one and the panel says so. Clients stay where they are placed:
// they are placed again only when their node leaves the group, is turned off or deleted, or
// the admin redistributes the group.

type Balancer struct {
	st  *store.Store
	now func() time.Time
	log *slog.Logger
	mu  sync.Mutex // places one client at a time: two at once could overfill a node
}

func NewBalancer(st *store.Store, now func() time.Time, log *slog.Logger) *Balancer {
	return &Balancer{st: st, now: now, log: log}
}

// GroupLoad is a balance group as the admin panel shows it.
type GroupLoad struct {
	Group string
	Nodes []NodeLoad
}

type NodeLoad struct {
	NodeID   int64
	Clients  int64
	Capacity sql.NullInt64
}

// Full: no room left on the node.
func (n NodeLoad) Full() bool { return n.Capacity.Valid && n.Clients >= n.Capacity.Int64 }

// Nodes narrows the nodes a client's subscription shows (on: the enabled ones) to one node
// per balance group: the client's, placing the client first when it has none there yet.
// Nodes outside every group are left as they are.
func (b *Balancer) Nodes(ctx context.Context, userID int64, on map[int64]bool) (map[int64]bool, error) {
	return b.nodes(ctx, userID, on, true)
}

// Peek is Nodes that places nobody: a client not placed in a group yet is shown the node
// Nodes would place it on now, and nothing is written. What the admin panel's preview of a
// subscription reads.
func (b *Balancer) Peek(ctx context.Context, userID int64, on map[int64]bool) (map[int64]bool, error) {
	return b.nodes(ctx, userID, on, false)
}

// nodes is Nodes; place: a client not placed yet is placed (written), else only picked.
func (b *Balancer) nodes(ctx context.Context, userID int64, on map[int64]bool, place bool) (map[int64]bool, error) {
	nodes, err := b.st.Q.ListNodes(ctx)
	if err != nil {
		return on, err
	}
	groups := map[string][]db.Node{}
	for _, n := range nodes {
		if n.BalanceGroup != "" && on[n.ID] {
			groups[n.BalanceGroup] = append(groups[n.BalanceGroup], n)
		}
	}
	if len(groups) == 0 {
		return on, nil
	}
	assigned := func() (map[string]int64, error) {
		held, err := b.st.Q.ListUserAssignments(ctx, userID)
		if err != nil {
			return nil, err
		}
		mine := map[string]int64{}
		for _, a := range held {
			mine[a.BalanceGroup] = a.NodeID
		}
		return mine, nil
	}
	mine, err := assigned()
	if err != nil {
		return on, err
	}
	// A client placed in every group is served without the lock: only a placement waits for
	// the others, and reads again what they placed meanwhile.
	placed := true
	for group, members := range groups {
		placed = placed && slices.ContainsFunc(members, func(n db.Node) bool { return n.ID == mine[group] })
	}
	if !placed && place {
		b.mu.Lock()
		defer b.mu.Unlock()
		if mine, err = assigned(); err != nil {
			return on, err
		}
	}
	var loads map[int64]int64
	out := map[int64]bool{}
	for id := range on {
		out[id] = true
	}
	for group, members := range groups {
		keep := mine[group]
		ok := false
		for _, n := range members {
			ok = ok || n.ID == keep
		}
		if !ok {
			if loads == nil {
				if loads, err = b.loads(ctx); err != nil {
					return on, err
				}
			}
			keep = pick(members, loads)
			if place {
				if err := b.st.Q.AssignNode(ctx, db.AssignNodeParams{UserID: userID, BalanceGroup: group, NodeID: keep, AssignedAt: b.now().Unix()}); err != nil {
					return on, err
				}
			}
			loads[keep]++
		}
		for _, n := range members {
			if n.ID != keep {
				delete(out, n.ID)
			}
		}
	}
	return out, nil
}

// pick is the node of a group with the most room left: the one with the lowest share of
// its capacity taken, an unlimited node by its client count, the lowest id on a tie.
func pick(members []db.Node, loads map[int64]int64) int64 {
	type cand struct {
		id   int64
		full bool
		used float64
	}
	cs := make([]cand, 0, len(members))
	for _, n := range members {
		c := cand{id: n.ID, used: float64(loads[n.ID])}
		if n.Capacity.Valid && n.Capacity.Int64 > 0 {
			c.used = float64(loads[n.ID]) / float64(n.Capacity.Int64)
			c.full = loads[n.ID] >= n.Capacity.Int64
		}
		cs = append(cs, c)
	}
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].full != cs[j].full {
			return !cs[i].full
		}
		if cs[i].used != cs[j].used {
			return cs[i].used < cs[j].used
		}
		return cs[i].id < cs[j].id
	})
	return cs[0].id
}

func (b *Balancer) loads(ctx context.Context) (map[int64]int64, error) {
	rows, err := b.st.Q.NodeLoads(ctx, sql.NullInt64{Int64: b.now().Unix(), Valid: true})
	if err != nil {
		return nil, err
	}
	out := make(map[int64]int64, len(rows))
	for _, r := range rows {
		out[r.NodeID] = r.Clients
	}
	return out, nil
}

// Loads are the balance groups and how full their nodes are.
func (b *Balancer) Loads(ctx context.Context) ([]GroupLoad, error) {
	nodes, err := b.st.Q.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	loads, err := b.loads(ctx)
	if err != nil {
		return nil, err
	}
	by := map[string]*GroupLoad{}
	var names []string
	for _, n := range nodes {
		if n.BalanceGroup == "" {
			continue
		}
		g := by[n.BalanceGroup]
		if g == nil {
			g = &GroupLoad{Group: n.BalanceGroup}
			by[n.BalanceGroup] = g
			names = append(names, n.BalanceGroup)
		}
		g.Nodes = append(g.Nodes, NodeLoad{NodeID: n.ID, Clients: loads[n.ID], Capacity: n.Capacity})
	}
	sort.Strings(names)
	out := make([]GroupLoad, 0, len(names))
	for _, name := range names {
		out = append(out, *by[name])
	}
	return out, nil
}

// SetNode puts a node in a balance group with room for capacity clients (invalid: no
// limit); group "" takes it out. Its clients from another group are placed again.
func (b *Balancer) SetNode(ctx context.Context, id int64, group string, capacity sql.NullInt64) error {
	return b.st.Tx(ctx, func(q *db.Queries) error {
		if err := q.SetNodeBalance(ctx, db.SetNodeBalanceParams{Capacity: capacity, BalanceGroup: group, UpdatedAt: b.now().Unix(), ID: id}); err != nil {
			return err
		}
		_, err := q.ClearStaleAssignments(ctx)
		return err
	})
}

// Redistribute forgets where the clients of a group are: each is placed again, on the node
// with the most room, the next time its subscription is fetched.
func (b *Balancer) Redistribute(ctx context.Context, group string) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.st.Q.ClearGroupAssignments(ctx, group)
}
