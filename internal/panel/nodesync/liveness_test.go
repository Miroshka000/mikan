package nodesync

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/events"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

func nodeEvents(t *testing.T, st *store.Store) []db.AddonEvent {
	t.Helper()
	evs, err := st.Q.ListAddonEvents(context.Background(), db.ListAddonEventsParams{Types: []string{}, Lim: 100})
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func eventTypes(evs []db.AddonEvent) []string {
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.Type)
	}
	return out
}

// A node is told down once, after three failed checks in a row, however long it stays down,
// and up once after two checks that pass; the checks go on every five seconds.
func TestNodeDownAndUpAreToldOnce(t *testing.T) {
	s, fake, st, _, now := setup(t)
	hub := events.NewHub()
	s.m.SetEvents(hub)
	ctx := context.Background()
	n := &sick{fakeNode: fake}
	s.node = n
	check := func(times int) {
		for range times {
			*now = now.Add(5 * time.Second)
			s.refreshHealth(ctx)
		}
	}
	timeout := errors.Join(nodeapi.ErrUnavailable, context.DeadlineExceeded)

	// A node that answers has nothing to take back.
	check(2)
	if evs := nodeEvents(t, st); len(evs) != 0 {
		t.Fatalf("events of a healthy node: %v", eventTypes(evs))
	}

	// A lost packet or two is not an outage.
	n.err = timeout
	check(2)
	n.err = nil
	check(1)
	n.err = timeout
	check(2)
	if evs := nodeEvents(t, st); len(evs) != 0 {
		t.Fatalf("events of two failures in a row: %v", eventTypes(evs))
	}

	woken := hub.Wait()
	check(1) // the third in a row
	select {
	case <-woken:
	default:
		t.Fatal("a waiting reader is not woken")
	}
	evs := nodeEvents(t, st)
	if len(evs) != 1 || evs[0].Type != events.NodeDown || evs[0].UserID.Valid || evs[0].TgID.Valid {
		t.Fatalf("events of the third failure: %+v", evs)
	}
	var down events.Node
	if err := json.Unmarshal([]byte(evs[0].Data), &down); err != nil {
		t.Fatal(err)
	}
	node, err := st.Q.GetNode(ctx, LocalNode)
	if err != nil {
		t.Fatal(err)
	}
	downSince := s.Health().Since
	if down.NodeID != LocalNode || down.Name != node.Name || down.Code != nodeapi.LinkTimeout || down.Since == 0 || down.Since > now.Unix() {
		t.Fatalf("node.down: %+v", down)
	}
	if down.Since != s.alive.since.Unix() || downSince.IsZero() {
		t.Fatalf("down since %d, the run of failures began %v", down.Since, s.alive.since)
	}

	// An hour of failures tells nothing more, and neither does another kind of failure.
	check(10)
	n.err = errors.Join(nodeapi.ErrUnavailable, errors.New("connection refused"))
	check(10)
	if evs := nodeEvents(t, st); len(evs) != 1 {
		t.Fatalf("a node that stays down told %v", eventTypes(evs))
	}

	// A restart of the panel forgets the counts, not what was told: down again, still one.
	s.alive = liveness{}
	check(5)
	if evs := nodeEvents(t, st); len(evs) != 1 {
		t.Fatalf("after a restart %v", eventTypes(evs))
	}

	// Back: told once, with when it went down.
	n.err = nil
	check(1)
	check(5)
	evs = nodeEvents(t, st)
	if !slices.Equal(eventTypes(evs), []string{events.NodeDown, events.NodeUp}) {
		t.Fatalf("events after it came back: %v", eventTypes(evs))
	}
	var up events.Node
	if err := json.Unmarshal([]byte(evs[1].Data), &up); err != nil {
		t.Fatal(err)
	}
	if up.NodeID != LocalNode || up.Name != node.Name || up.DownAt != down.Since {
		t.Fatalf("node.up: %+v (went down %d)", up, down.Since)
	}

	// And down again later is a new outage, told again.
	n.err = timeout
	check(3)
	evs = nodeEvents(t, st)
	if !slices.Equal(eventTypes(evs), []string{events.NodeDown, events.NodeUp, events.NodeDown}) {
		t.Fatalf("events of a second outage: %v", eventTypes(evs))
	}
	var again events.Node
	if err := json.Unmarshal([]byte(evs[2].Data), &again); err != nil || again.Since <= down.Since {
		t.Fatalf("second node.down: %+v %v", again, err)
	}
}

// A check cut short by the panel stopping says nothing of the node.
func TestNodeHealthCheckCutShortTellsNothing(t *testing.T) {
	s, fake, st, _, now := setup(t)
	n := &sick{fakeNode: fake, err: errors.Join(nodeapi.ErrUnavailable, context.Canceled)}
	s.node = n
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for range 5 {
		*now = now.Add(5 * time.Second)
		s.refreshHealth(ctx)
	}
	if evs := nodeEvents(t, st); len(evs) != 0 {
		t.Fatalf("events of a cancelled check: %v", eventTypes(evs))
	}
}

// A node the admin removed while it was down is told of to no one.
func TestNodeRemovedWhileDownTellsNothing(t *testing.T) {
	s, fake, st, _, now := setup(t)
	ctx := context.Background()
	n := &sick{fakeNode: fake, err: errors.Join(nodeapi.ErrUnavailable, context.DeadlineExceeded)}
	s.node = n
	s.id = 9999 // not in the table
	for range 4 {
		*now = now.Add(5 * time.Second)
		s.refreshHealth(ctx)
	}
	if evs := nodeEvents(t, st); len(evs) != 0 {
		t.Fatalf("events of a node that is not there: %v", eventTypes(evs))
	}
}
