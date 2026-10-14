package nodesync

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"mikan/internal/panel/events"
	"mikan/internal/panel/hysteresis"
	"mikan/internal/panel/store/db"
)

// liveness turns the node's health checks into the two events of its life, events.NodeDown
// and events.NodeUp, once per change however long the checks go on. The hysteresis is the
// admin's alerts' (hysteresis.Tracker: three failed checks to go down, two good ones to come
// back, five seconds apart), so the addons and the admin's chat hear of an outage at the same
// moment. What was told is kept in node_state (key "health/<node>": "up", or "down:<unix
// seconds the failures began>"), so a restart of the panel neither tells a node that is still
// down again nor loses that it came back. A node never told down needs no word when it
// answers: nothing was said to take back.
type liveness struct {
	mu    sync.Mutex
	read  bool               // told is what node_state holds
	told  string             // "", "up" or "down:<since>"
	track hysteresis.Tracker // seeded from told
	since time.Time          // when the run of failures began
}

func (l *liveness) down() (since int64, ok bool) {
	rest, ok := strings.CutPrefix(l.told, "down:")
	if !ok {
		return 0, false
	}
	since, err := strconv.ParseInt(rest, 10, 64)
	return since, err == nil
}

// observe takes one health check of the node. A failure to read or write what was told is
// logged and tried again with the next check.
func (s *Syncer) observe(ctx context.Context, view *HealthView) {
	// A check cut short by the panel stopping says nothing of the node.
	if ctx.Err() != nil {
		return
	}
	l := &s.alive
	l.mu.Lock()
	defer l.mu.Unlock()
	key := stateKeyOf("health", s.id)
	if !l.read {
		v, err := s.m.st.Q.GetNodeState(ctx, key)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			s.log.Warn("node health: what was told is unreadable", "err", err)
			return
		}
		l.told, l.read = v, true
		// A node told down is still down for the tracker: it comes back after the good checks.
		if since, down := l.down(); down {
			l.track.Level, l.since = hysteresis.Unavailable, time.Unix(since, 0)
		}
	}
	level := hysteresis.Healthy
	if !view.OK {
		level = hysteresis.Unavailable
		if l.since.IsZero() {
			l.since = view.CheckedAt
		}
	} else if l.track.Level != hysteresis.Unavailable {
		l.since = time.Time{} // a lost packet is over: the next run of failures starts anew
	}
	before := l.track
	tr := l.track.Observe(level, hysteresis.NodeFailAfter, hysteresis.RecoverAfter)
	if tr == nil {
		return
	}
	var ok bool
	switch tr.To {
	case hysteresis.Unavailable:
		ok = s.tell(ctx, key, events.NodeDown, "down:"+strconv.FormatInt(l.since.Unix(), 10),
			events.Node{NodeID: s.id, Code: view.Code, Since: l.since.Unix()}, "node.down:"+strconv.FormatInt(s.id, 10)+":"+strconv.FormatInt(l.since.Unix(), 10))
	case hysteresis.Healthy:
		since, wasDown := l.down()
		if !wasDown {
			return // never told down: nothing to take back
		}
		ok = s.tell(ctx, key, events.NodeUp, "up",
			events.Node{NodeID: s.id, DownAt: since}, "node.up:"+strconv.FormatInt(s.id, 10)+":"+strconv.FormatInt(since, 10))
		if ok {
			l.since = time.Time{}
		}
	}
	if !ok {
		// Not told: the tracker steps back to just before the change, so the next check tells it.
		l.track = before
	}
}

// tell writes the change and its event on one transaction, so what is told and what is kept
// agree; the node's name is read there too. A node removed meanwhile is told of to no one.
func (s *Syncer) tell(ctx context.Context, key, typ, next string, data events.Node, dedupe string) bool {
	err := s.m.st.Tx(ctx, func(q *db.Queries) error {
		n, err := q.GetNode(ctx, s.id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		data.Name = n.Name
		if err := q.SetNodeState(ctx, db.SetNodeStateParams{Key: key, Value: next}); err != nil {
			return err
		}
		return events.Emit(ctx, q, s.m.now(), typ, 0, 0, data, dedupe)
	})
	if err != nil {
		s.log.Warn("node health: the change is not told", "event", typ, "err", err)
		return false
	}
	s.alive.told = next
	s.m.hub.Kick()
	return true
}
