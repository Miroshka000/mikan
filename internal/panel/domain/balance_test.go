package domain

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"mikan/internal/panel/store/db"
	"mikan/internal/panel/store/storetest"
)

// Clients of a balance group are spread over its nodes by the room they have, each kept
// on its node; a full group still takes clients, a node turned off has its clients placed
// again, and redistributing places everyone anew.
func TestBalancer(t *testing.T) {
	ctx := context.Background()
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Unix(1_800_000_000, 0)
	b := NewBalancer(st, func() time.Time { return now }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	node := func(name string, capacity int64) db.Node {
		t.Helper()
		n, err := st.Q.CreateNode(ctx, db.CreateNodeParams{Name: name, Address: name + ":1", PublicHost: "203.0.113.1", CreatedAt: 1, UpdatedAt: 1})
		if err != nil {
			t.Fatal(err)
		}
		c := sql.NullInt64{Int64: capacity, Valid: capacity > 0}
		if err := b.SetNode(ctx, n.ID, "eu", c); err != nil {
			t.Fatal(err)
		}
		return n
	}
	a, big := node("a", 2), node("big", 4)
	solo, err := st.Q.CreateNode(ctx, db.CreateNodeParams{Name: "solo", Address: "solo:1", PublicHost: "203.0.113.9", CreatedAt: 1, UpdatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	on := map[int64]bool{1: true, a.ID: true, big.ID: true, solo.ID: true}
	user := func(i int) int64 {
		t.Helper()
		u, err := st.Q.CreateUser(ctx, db.CreateUserParams{Name: fmt.Sprint("u", i), SubToken: fmt.Sprint("tok", i), PeriodStart: 1, CreatedAt: 1, UpdatedAt: 1,
			ResetStrategy: "none", Tags: "[]", Source: "admin"})
		if err != nil {
			t.Fatal(err)
		}
		return u.ID
	}
	// Peek shows the node a new client would be placed on, and places it nowhere.
	peeked := user(100)
	got, err := b.Peek(ctx, peeked, on)
	if err != nil || !got[1] || !got[solo.ID] || got[a.ID] == got[big.ID] {
		t.Fatalf("peek: one node of the group and every other: %v %v", got, err)
	}
	if held, err := st.Q.ListUserAssignments(ctx, peeked); err != nil || len(held) != 0 {
		t.Fatalf("peek placed the client: %+v %v", held, err)
	}
	placed := map[int64]int{}
	where := map[int64]int64{}
	for i := range 6 {
		id := user(i)
		got, err := b.Nodes(ctx, id, on)
		if err != nil {
			t.Fatal(err)
		}
		if !got[1] || !got[solo.ID] || got[a.ID] == got[big.ID] {
			t.Fatalf("client %d: one node of the group and every other: %v", i, got)
		}
		n := a.ID
		if got[big.ID] {
			n = big.ID
		}
		placed[n]++
		where[id] = n
	}
	if placed[a.ID] != 2 || placed[big.ID] != 4 {
		t.Fatalf("by room: %v", placed)
	}
	// The same client stays where it is.
	for id, n := range where {
		if got, _ := b.Nodes(ctx, id, on); !got[n] {
			t.Fatalf("client %d moved off %d: %v", id, n, got)
		}
	}
	// Full: the next one goes to the least loaded node anyway.
	if got, err := b.Nodes(ctx, user(7), on); err != nil || got[a.ID] == got[big.ID] {
		t.Fatalf("a full group: %v %v", got, err)
	}
	loads, err := b.Loads(ctx)
	if err != nil || len(loads) != 1 || len(loads[0].Nodes) != 2 {
		t.Fatalf("loads: %+v %v", loads, err)
	}
	// A node turned off: its clients go to the one left.
	off := map[int64]bool{1: true, big.ID: true, solo.ID: true}
	for id, n := range where {
		if n != a.ID {
			continue
		}
		if got, _ := b.Nodes(ctx, id, off); !got[big.ID] {
			t.Fatalf("client %d of a node turned off: %v", id, got)
		}
	}
	if n, err := b.Redistribute(ctx, "eu"); err != nil || n != 7 {
		t.Fatalf("redistribute: %d %v", n, err)
	}
	// Out of the group: the node shows to everyone again.
	if err := b.SetNode(ctx, a.ID, "", sql.NullInt64{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := b.Nodes(ctx, user(8), on); !got[a.ID] || !got[big.ID] {
		t.Fatalf("a node out of the group: %v", got)
	}
}
