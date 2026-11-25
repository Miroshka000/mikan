package api

import (
	"slices"
	"testing"

	"mikan/internal/panel/store/db"
)

// A node goes where its first server does; one without servers after the others, in the
// order it had.
func TestNodesByServers(t *testing.T) {
	nodes := []db.Node{{ID: 1}, {ID: 5}, {ID: 3}, {ID: 9}}
	for _, c := range []struct {
		servers, want []int64
	}{
		{nil, []int64{1, 5, 3, 9}},
		{[]int64{3, 3, 1, 3, 5}, []int64{3, 1, 5, 9}},
		{[]int64{9, 1, 9}, []int64{9, 1, 5, 3}},
		{[]int64{7, 5}, []int64{5, 1, 3, 9}}, // a node gone meanwhile is no node
	} {
		if got := nodesByServers(c.servers, nodes); !slices.Equal(got, c.want) {
			t.Errorf("servers %v: %v, want %v", c.servers, got, c.want)
		}
	}
}

// The servers go node by node in the nodes' order, a node's as they were; a node the order
// does not list goes last.
func TestServersByNodes(t *testing.T) {
	row := func(host, node int64) db.ListHostsInOrderRow {
		return db.ListHostsInOrderRow{InboundHost: db.InboundHost{ID: host}, NodeID: node}
	}
	rows := []db.ListHostsInOrderRow{row(10, 2), row(11, 1), row(12, 2), row(13, 3), row(14, 1)}
	if got, want := serversByNodes(rows, []int64{1, 2, 3}), []int64{11, 14, 10, 12, 13}; !slices.Equal(got, want) {
		t.Errorf("by 1 2 3: %v, want %v", got, want)
	}
	if got, want := serversByNodes(rows, []int64{3, 2}), []int64{13, 10, 12, 11, 14}; !slices.Equal(got, want) {
		t.Errorf("by 3 2: %v, want %v", got, want)
	}
	if rows[0].InboundHost.ID != 10 {
		t.Error("the rows given are not moved")
	}
}
