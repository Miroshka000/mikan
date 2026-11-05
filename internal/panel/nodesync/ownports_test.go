package nodesync

import (
	"context"
	"slices"
	"testing"

	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

// The panel's own node keeps the panel and the subscription open to its users; another node
// has no need of them. 80 and 443 stay open on every node, and each node adds the admin's
// ports of its own alone.
func TestOwnPortsInTheState(t *testing.T) {
	local, _, st, _, _ := setup(t)
	ctx := context.Background()
	remote, _ := addRemote(t, local, st)
	set := settings.New(st.Q)
	if err := settings.Set(ctx, set, settings.KeyPanelPort, 2053); err != nil {
		t.Fatal(err)
	}
	check := func(state string, s *Syncer, want ...int) {
		t.Helper()
		got, err := s.desired(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got.OwnPorts, want) {
			t.Fatalf("%s: %v, want %v", state, got.OwnPorts, want)
		}
	}
	check("the panel's node", local, 80, 443, 2053)
	check("a remote node", remote, 80, 443)

	if err := settings.Set(ctx, set, settings.KeySubPort, 2083); err != nil {
		t.Fatal(err)
	}
	check("the panel's node with a subscription port", local, 80, 443, 2053, 2083)
	check("a remote node with a subscription port", remote, 80, 443)
	if err := settings.Set(ctx, set, settings.KeySubPort, 0); err != nil {
		t.Fatal(err)
	}
	check("the subscription back on the panel's port", local, 80, 443, 2053)

	// The admin's ports are that node's, and a port listed twice goes out once.
	if err := st.Q.SetNodeOwnPorts(ctx, db.SetNodeOwnPortsParams{OwnPorts: []int64{53, 443, 2053}, ID: remote.id}); err != nil {
		t.Fatal(err)
	}
	check("a remote node with the admin's ports", remote, 53, 80, 443, 2053)
	check("the panel's node beside it", local, 80, 443, 2053)
	if err := st.Q.SetNodeOwnPorts(ctx, db.SetNodeOwnPortsParams{OwnPorts: []int64{53, 2053}, ID: local.id}); err != nil {
		t.Fatal(err)
	}
	check("the panel's node with the admin's ports", local, 53, 80, 443, 2053)
}

// A node updated after the panel holds the state as its old version saved it, without what
// that version did not know: the list of ports, and so the guard, would stay away until
// something else changed the state. It takes the whole state again at once, and only then.
func TestUpdatedNodeTakesTheStateAgain(t *testing.T) {
	s, node, _, _, _ := setup(t)
	ctx := context.Background()
	s.applyState(ctx)
	pushed := len(node.applied)
	if pushed == 0 {
		t.Fatal("the first state did not go out")
	}
	report := func(version string) {
		t.Helper()
		node.health.Version, node.health.Revision = version, node.applied[len(node.applied)-1].Revision
		s.refreshHealth(ctx)
		select {
		case <-s.stateDirty:
		default:
		}
		s.applyState(ctx)
	}
	report("0.5.2.0")
	report("0.5.2.0")
	if len(node.applied) != pushed {
		t.Fatalf("the same version took the state again: %d pushes, want %d", len(node.applied), pushed)
	}
	report("0.5.2.1")
	if len(node.applied) != pushed+1 {
		t.Fatalf("the updated node: %d pushes, want %d", len(node.applied), pushed+1)
	}
	report("0.5.2.1")
	if len(node.applied) != pushed+1 {
		t.Fatalf("after the update: %d pushes, want %d", len(node.applied), pushed+1)
	}
}

// The list reaches a node that is already in line: a new one is a new state.
func TestOwnPortsMoveTheStateKey(t *testing.T) {
	local, _, st, _, _ := setup(t)
	ctx := context.Background()
	before, err := local.desired(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Q.SetNodeOwnPorts(ctx, db.SetNodeOwnPortsParams{OwnPorts: []int64{53}, ID: local.id}); err != nil {
		t.Fatal(err)
	}
	after, err := local.desired(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stateKey(before) == stateKey(after) {
		t.Fatal("the state key does not see the ports")
	}
}
