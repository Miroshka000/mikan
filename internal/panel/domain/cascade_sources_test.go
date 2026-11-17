package domain

import (
	"context"
	"database/sql"
	"slices"
	"testing"
	"time"

	"mikan/internal/panel/store/db"
)

func srcIDs(users []db.RelayUser) []int64 {
	out := []int64{}
	for _, u := range users {
		out = append(out, u.SrcNodeID)
	}
	return out
}

func csNode(id int64, on bool) db.Node {
	n := db.Node{ID: id}
	if on {
		n.Enabled = 1
	}
	return n
}

func csInbound(node, exit int64, on bool) db.Inbound {
	in := db.Inbound{NodeID: node, ExitNodeID: sql.NullInt64{Int64: exit, Valid: exit != 0}, Outbound: "direct"}
	if on {
		in.Enabled = 1
	}
	return in
}

func csRelay(node, exit int64) db.NodeRelay {
	return db.NodeRelay{NodeID: node, Outbound: "direct", ExitNodeID: sql.NullInt64{Int64: exit, Valid: exit != 0}}
}

func csKeys(exit int64, srcs ...int64) []db.RelayUser {
	var out []db.RelayUser
	for _, s := range srcs {
		out = append(out, db.RelayUser{ExitNodeID: exit, SrcNodeID: s, Uuid: "k"})
	}
	return out
}

// The sources of a relay are the nodes that send traffic through it now: an enabled protocol
// of an enabled node with that exit, or a relay that goes on to it and has sources itself. A
// key left from an earlier choice is no source.
func TestRelaySources(t *testing.T) {
	const a, b, c, d = 1, 2, 3, 4
	for name, tc := range map[string]struct {
		nodes    []db.Node
		inbounds []db.Inbound
		relays   []db.NodeRelay
		exit     int64
		users    []db.RelayUser
		want     []int64
	}{
		"a protocol leaves through it": {
			nodes: []db.Node{csNode(a, true), csNode(b, true)}, inbounds: []db.Inbound{csInbound(a, b, true)},
			exit: b, users: csKeys(b, a), want: []int64{a},
		},
		"the protocol is off": {
			nodes: []db.Node{csNode(a, true), csNode(b, true)}, inbounds: []db.Inbound{csInbound(a, b, false)},
			exit: b, users: csKeys(b, a), want: []int64{},
		},
		"the protocol leaves directly now": {
			nodes: []db.Node{csNode(a, true), csNode(b, true)}, inbounds: []db.Inbound{csInbound(a, 0, true)},
			exit: b, users: csKeys(b, a), want: []int64{},
		},
		"the source node is off": {
			nodes: []db.Node{csNode(a, false), csNode(b, true)}, inbounds: []db.Inbound{csInbound(a, b, true)},
			exit: b, users: csKeys(b, a), want: []int64{},
		},
		"a relay without sources of its own goes on to it": {
			// The Cascade window of A sends A's relay to B, and nobody leaves through A.
			nodes: []db.Node{csNode(a, true), csNode(b, true)}, relays: []db.NodeRelay{csRelay(a, b), csRelay(b, 0)},
			exit: b, users: csKeys(b, a), want: []int64{},
		},
		"a chain A → B → C": {
			nodes:    []db.Node{csNode(a, true), csNode(b, true), csNode(c, true)},
			inbounds: []db.Inbound{csInbound(a, b, true)}, relays: []db.NodeRelay{csRelay(b, c), csRelay(c, 0)},
			exit: c, users: csKeys(c, b), want: []int64{b},
		},
		"the chain after A left": {
			nodes:    []db.Node{csNode(a, true), csNode(b, true), csNode(c, true)},
			inbounds: []db.Inbound{csInbound(a, b, false)}, relays: []db.NodeRelay{csRelay(b, c), csRelay(c, 0)},
			exit: c, users: csKeys(c, b), want: []int64{},
		},
		"the chain after A was switched off": {
			nodes:    []db.Node{csNode(a, false), csNode(b, true), csNode(c, true)},
			inbounds: []db.Inbound{csInbound(a, b, true)}, relays: []db.NodeRelay{csRelay(b, c), csRelay(c, 0)},
			exit: c, users: csKeys(c, b), want: []int64{},
		},
		"a loop of relays with nobody in it": {
			// The database never holds one (CheckExit), but the walk must end all the same.
			nodes:  []db.Node{csNode(a, true), csNode(b, true), csNode(c, true)},
			relays: []db.NodeRelay{csRelay(a, b), csRelay(b, c), csRelay(c, a)},
			exit:   c, users: csKeys(c, b), want: []int64{},
		},
		"only the keys of nodes that use it": {
			nodes:    []db.Node{csNode(a, true), csNode(b, true), csNode(c, true), csNode(d, true)},
			inbounds: []db.Inbound{csInbound(a, d, true), csInbound(b, c, true), csInbound(c, d, true)},
			exit:     d, users: csKeys(d, a, b, c), want: []int64{a, c},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := srcIDs(NewCascade(tc.nodes, tc.inbounds, tc.relays).RelaySources(tc.exit, tc.users))
			if !slices.Equal(got, tc.want) {
				t.Fatalf("sources %v, want %v", got, tc.want)
			}
		})
	}
}

// A chain longer than maxChain has no sources at its far end: the walk stops there.
func TestRelaySourcesStopAtMaxChain(t *testing.T) {
	var nodes []db.Node
	var relays []db.NodeRelay
	last := int64(maxChain + 3)
	for id := int64(1); id <= last; id++ {
		nodes = append(nodes, csNode(id, true))
		if id > 1 && id < last {
			relays = append(relays, csRelay(id, id+1))
		}
	}
	c := NewCascade(nodes, []db.Inbound{csInbound(1, 2, true)}, relays)
	if got := srcIDs(c.RelaySources(3, csKeys(3, 2))); !slices.Equal(got, []int64{2}) {
		t.Fatalf("near: %v", got)
	}
	if got := srcIDs(c.RelaySources(last, csKeys(last, last-1))); len(got) != 0 {
		t.Fatalf("beyond maxChain: %v", got)
	}
}

// A source that leaves stops being one, its key stays; when it comes back it uses the same key.
func TestRelaySourceLeavesAndComesBack(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, _, _ := setup(t, &now)
	ctx := context.Background()
	q := st.Q
	x, err := q.CreateNode(ctx, db.CreateNodeParams{Name: "B", Address: "198.51.100.20:40000", PublicHost: "198.51.100.20", CreatedAt: now.Unix(), UpdatedAt: now.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	ins, err := q.ListNodeInbounds(ctx, 1)
	if err != nil || len(ins) == 0 {
		t.Fatalf("inbounds of node 1: %v %v", ins, err)
	}
	in := ins[0]
	if in.Enabled == 0 {
		t.Fatal("the seeded inbound is off")
	}
	setExit := func(exit int64) {
		t.Helper()
		if exit != 0 {
			if err := UseExit(ctx, q, 1, exit, now, nil); err != nil {
				t.Fatal(err)
			}
		}
		if err := q.SetInboundExit(ctx, db.SetInboundExitParams{ExitNodeID: sql.NullInt64{Int64: exit, Valid: exit != 0}, Outbound: "direct", ID: in.ID}); err != nil {
			t.Fatal(err)
		}
	}
	sources := func() []db.RelayUser {
		t.Helper()
		got, err := CurrentRelaySources(ctx, q, x.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	setExit(x.ID)
	first := sources()
	if len(first) != 1 || first[0].SrcNodeID != 1 {
		t.Fatalf("through B: %+v", first)
	}
	setExit(0)
	if got := sources(); len(got) != 0 {
		t.Fatalf("after it left: %+v", got)
	}
	if users, err := q.ListRelayUsers(ctx, x.ID); err != nil || len(users) != 1 {
		t.Fatalf("the key must stay: %+v %v", users, err)
	}
	setExit(x.ID)
	if got := sources(); len(got) != 1 || got[0].Uuid != first[0].Uuid {
		t.Fatalf("back with another key: %+v, was %+v", got, first)
	}
}
