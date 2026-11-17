package domain

import (
	"context"
	"slices"

	"mikan/internal/panel/store/db"
)

// Cascade tells which nodes send traffic out through which now. A key at a relay
// (relay_users) stays when its node stops leaving through it, so that it is the same when
// the node comes back: the keys alone say who did once, not who does.
type Cascade struct {
	on       map[int64]bool           // the enabled nodes
	inbounds map[int64]map[int64]bool // exit → the nodes with an enabled inbound leaving through it
	relays   map[int64][]int64        // exit → the nodes whose relay goes on to it
}

// NewCascade reads the nodes, all inbounds and all relays, as the caller already has them.
func NewCascade(nodes []db.Node, inbounds []db.Inbound, relays []db.NodeRelay) *Cascade {
	c := &Cascade{on: map[int64]bool{}, inbounds: map[int64]map[int64]bool{}, relays: map[int64][]int64{}}
	for _, n := range nodes {
		c.on[n.ID] = n.Enabled != 0
	}
	for _, in := range inbounds {
		if in.Enabled == 0 || !in.ExitNodeID.Valid {
			continue
		}
		x := in.ExitNodeID.Int64
		if c.inbounds[x] == nil {
			c.inbounds[x] = map[int64]bool{}
		}
		c.inbounds[x][in.NodeID] = true
	}
	for _, r := range relays {
		if r.ExitNodeID.Valid {
			c.relays[r.ExitNodeID.Int64] = append(c.relays[r.ExitNodeID.Int64], r.NodeID)
		}
	}
	return c
}

// LoadCascade reads what NewCascade needs.
func LoadCascade(ctx context.Context, q *db.Queries) (*Cascade, error) {
	ins, err := q.ListInbounds(ctx)
	if err != nil {
		return nil, err
	}
	return LoadCascadeWith(ctx, q, ins)
}

// LoadCascadeWith is LoadCascade with the inbounds the caller has read already.
func LoadCascadeWith(ctx context.Context, q *db.Queries, inbounds []db.Inbound) (*Cascade, error) {
	nodes, err := q.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	relays, err := q.ListNodeRelays(ctx)
	if err != nil {
		return nil, err
	}
	return NewCascade(nodes, inbounds, relays), nil
}

// CurrentRelaySources are the keys at node exit's relay whose nodes leave through it now.
func CurrentRelaySources(ctx context.Context, q *db.Queries, exit int64) ([]db.RelayUser, error) {
	c, err := LoadCascade(ctx, q)
	if err != nil {
		return nil, err
	}
	users, err := q.ListRelayUsers(ctx, exit)
	if err != nil {
		return nil, err
	}
	return c.RelaySources(exit, users), nil
}

// RelaySources are those of users, the keys at node exit's relay, whose node is on and leaves
// through exit now: by an enabled inbound, or by its own relay going on to exit while that
// relay has sources itself.
func (c *Cascade) RelaySources(exit int64, users []db.RelayUser) []db.RelayUser {
	out := []db.RelayUser{}
	for _, u := range users {
		if u.ExitNodeID == exit && c.uses(u.SrcNodeID, exit, 1, map[int64]bool{exit: true}) {
			out = append(out, u)
		}
	}
	return out
}

// RelayLive says whether anyone leaves through node exit's relay now, keys aside: what the
// infrastructure alerts ask, which do not read the keys.
func (c *Cascade) RelayLive(exit int64) bool {
	return c.relayLive(exit, 0, map[int64]bool{})
}

// uses says whether node src sends traffic out through node exit now. depth is the hops so
// far, path the relays on the way, which a loop would come back to.
func (c *Cascade) uses(src, exit int64, depth int, path map[int64]bool) bool {
	if !c.on[src] || src == exit {
		return false
	}
	if c.inbounds[exit][src] {
		return true
	}
	if !slices.Contains(c.relays[exit], src) {
		return false
	}
	return c.relayLive(src, depth, path)
}

// relayLive says whether some node leaves through node x's relay now. A relay goes on to
// one node only, so the walk back from x visits each node once at most.
func (c *Cascade) relayLive(x int64, depth int, path map[int64]bool) bool {
	if depth >= maxChain || path[x] {
		return false
	}
	path[x] = true
	defer delete(path, x)
	for src := range c.inbounds[x] {
		if c.on[src] && src != x {
			return true
		}
	}
	for _, src := range c.relays[x] {
		if c.uses(src, x, depth+1, path) {
			return true
		}
	}
	return false
}
