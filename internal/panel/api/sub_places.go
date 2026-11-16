package api

import (
	"cmp"
	"context"
	"slices"

	"mikan/internal/panel/store/db"
)

// The places of the servers in the subscriptions (#300): every address for clients of every
// protocol has one (inbound_hosts.sort), across the nodes. A node goes where its first server
// does, so the panel's lists and the apps' groups of countries follow the servers; the order
// of the nodes moves their servers as blocks.

// placeServers gives the hosts the places ids lists them in, every host of the panel once
// (host_order_mismatch otherwise), and the nodes the places of their first servers. rows are
// the hosts as ListHostsInOrder reads them, nodes the nodes as ListNodes does.
func placeServers(ctx context.Context, q *db.Queries, ids []int64, rows []db.ListHostsInOrderRow, nodes []db.Node) error {
	have := make([]int64, len(rows))
	nodeOf := make(map[int64]int64, len(rows))
	for i, r := range rows {
		have[i] = r.InboundHost.ID
		nodeOf[r.InboundHost.ID] = r.NodeID
	}
	if err := setOrderAt("body.host_ids", ids, have, "host_order_mismatch", func(id, place int64) error {
		return q.SetHostSort(ctx, db.SetHostSortParams{Sort: place, ID: id})
	}); err != nil {
		return err
	}
	servers := make([]int64, len(ids))
	for i, id := range ids {
		servers[i] = nodeOf[id]
	}
	for i, id := range nodesByServers(servers, nodes) {
		if err := q.SetNodeSort(ctx, db.SetNodeSortParams{Sort: int64(i + 1), ID: id}); err != nil {
			return err
		}
	}
	return nil
}

// moveServers puts the servers of rows (ListHostsInOrder's) in the order of the nodes: node
// by node as order lists them, a node's servers as they were.
func moveServers(ctx context.Context, q *db.Queries, rows []db.ListHostsInOrderRow, order []int64) error {
	for i, id := range serversByNodes(rows, order) {
		if err := q.SetHostSort(ctx, db.SetHostSortParams{Sort: int64(i + 1), ID: id}); err != nil {
			return err
		}
	}
	return nil
}

// nodesByServers is the order of the nodes that servers, the node of each server in the
// subscription's order, put them in: a node goes where its first server does, one without
// servers after the others, in the order it had among nodes.
func nodesByServers(servers []int64, nodes []db.Node) []int64 {
	out := make([]int64, 0, len(nodes))
	seen := make(map[int64]bool, len(nodes))
	known := make(map[int64]bool, len(nodes))
	for _, n := range nodes {
		known[n.ID] = true
	}
	for _, id := range servers {
		if known[id] && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, n := range nodes {
		if !seen[n.ID] {
			seen[n.ID] = true
			out = append(out, n.ID)
		}
	}
	return out
}

// serversByNodes are the ids of the hosts of rows (in the subscription's order) as the
// nodes' order puts them: node by node as order lists them, a node's in the order they had.
// A node order does not list goes last.
func serversByNodes(rows []db.ListHostsInOrderRow, order []int64) []int64 {
	rank := make(map[int64]int, len(order))
	for i, id := range order {
		rank[id] = i
	}
	of := func(node int64) int {
		if r, ok := rank[node]; ok {
			return r
		}
		return len(order)
	}
	sorted := slices.Clone(rows)
	slices.SortStableFunc(sorted, func(a, b db.ListHostsInOrderRow) int { return cmp.Compare(of(a.NodeID), of(b.NodeID)) })
	out := make([]int64, len(sorted))
	for i, r := range sorted {
		out[i] = r.InboundHost.ID
	}
	return out
}
