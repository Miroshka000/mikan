package panelapi

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"mikan/internal/panel/addons"
	"mikan/internal/panel/events"
	"mikan/internal/panel/store/db"
)

// Push delivery of the feed of events (docs/pipeline/addons.md, 15.3): an addon that serves
// HTTP and says "push": true in its /v2/info is sent its events by the panel as
// POST /v2/events {"events": [...]} instead of pulling them, so a small addon needs no loop
// of its own. It is the pull's feed in everything else:
//
//   - the same cursor (addon_cursors), which moves only when the addon answered 2xx, so a
//     batch that was not taken is sent again, whole, and delivery stays at least once;
//   - the same permission (events: an addon without it is sent nothing) and the same filter:
//     the bot.* events only to the addon that holds the bot, and only the types the addon
//     named in its /v2/info (events), or all of them when it named none;
//   - batches of at most pushBatch events, in id order, one batch in flight per addon.
//
// A batch that fails (the addon does not answer or answers anything but 2xx) is sent again
// after a pause that doubles from one second to five minutes and starts over after a
// success. A worker lives only while its addon is running and pushed to: when it stops, the
// worker goes, and a new one starts with the new run, without the old pause.

const (
	pushBatch = 100
	pushFirst = time.Second
	pushMax   = 5 * time.Minute
	// pushScan is how often the supervisor looks for addons that came up. A worker of an addon
	// that is not pushed to ends at once, and is started again by the next scan.
	pushScan = 5 * time.Second
)

// pushTune lets the tests shorten the pauses; zero values are the constants above.
type pushTune struct{ first, max, scan time.Duration }

func (t pushTune) or() pushTune {
	if t.first == 0 {
		t.first = pushFirst
	}
	if t.max == 0 {
		t.max = pushMax
	}
	if t.scan == 0 {
		t.scan = pushScan
	}
	return t
}

// pushTarget is what a worker needs of its addon, asked again at every round: it is running,
// serves HTTP, asks for push and holds the events permission now.
type pushTarget struct {
	installed addons.Installed
	caller    Caller
	types     []string
}

func (s *Server) pushTarget(ctx context.Context, id string) (pushTarget, bool) {
	// The addon is asked its description once per build; a slow one must not hold the others.
	ictx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	a, info, ok := s.d.Addons.Pushes(ictx, id)
	if !ok {
		return pushTarget{}, false
	}
	perms, version, err := addons.LoadGrant(ctx, s.d.Store.Q, id)
	if err != nil || !addons.HasPerm(perms, "events") {
		return pushTarget{}, false
	}
	return pushTarget{installed: a, caller: Caller{ID: id, Version: version, Permissions: perms}, types: info.Events}, true
}

// RunPush keeps a worker for every running addon that is pushed to, until ctx ends.
func (s *Server) RunPush(ctx context.Context) {
	tune := s.push.or()
	var (
		mu      sync.Mutex
		workers = map[string]bool{}
		wg      sync.WaitGroup
	)
	defer wg.Wait()
	scan := func() {
		st, err := s.d.Addons.State()
		if err != nil {
			return
		}
		for id, a := range st.Adapters {
			if a.KindOf() == addons.KindPayment || a.ModeOf() != addons.ModeHTTP || a.Status != "running" || a.Listen == "" {
				continue
			}
			mu.Lock()
			busy := workers[id]
			mu.Unlock()
			if busy {
				continue
			}
			mu.Lock()
			workers[id] = true
			mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				// Asked here, not in the scan: an addon whose /v2/info hangs holds only its own
				// slot, and the scan goes on to the others. pushTo asks the same first.
				s.pushTo(ctx, id, tune)
				mu.Lock()
				delete(workers, id)
				mu.Unlock()
			}()
		}
	}
	scan()
	t := time.NewTicker(tune.scan)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			scan()
		}
	}
}

// pushTo sends the feed to one addon until it stops being a target or ctx ends.
func (s *Server) pushTo(ctx context.Context, id string, tune pushTune) {
	pause := time.Duration(0) // how long the next failure waits: 0 after a success
	for ctx.Err() == nil {
		tgt, ok := s.pushTarget(ctx, id)
		if !ok {
			return
		}
		cursor, err := s.d.Store.Q.GetAddonCursor(ctx, id)
		if err != nil {
			s.d.Log.Warn("addons push: cursor", "addon", id, "err", err)
			pause = nextPause(pause, tune)
			if !s.pushWait(ctx, id, pause) {
				return
			}
			continue
		}
		holder := s.holds(ctx, tgt.caller)
		// Waits up to a second for something new: the round then asks again whether the
		// addon is still there, which is how a worker notices its addon stopping.
		rows, err := longPoll(ctx, s.d.Hub, time.Second, func() ([]db.AddonEvent, error) {
			return s.d.Store.Q.ListAddonEvents(ctx, db.ListAddonEventsParams{After: cursor, Types: tgt.types, BotToo: holder, Lim: pushBatch})
		})
		if err != nil {
			if ctx.Err() == nil {
				s.d.Log.Warn("addons push: events", "addon", id, "err", err)
				pause = nextPause(pause, tune)
				if !s.pushWait(ctx, id, pause) {
					return
				}
			}
			continue
		}
		if len(rows) == 0 {
			continue
		}
		body, err := json.Marshal(struct {
			Events []events.Event `json:"events"`
		}{s.feedOf(rows, holder)})
		if err != nil {
			s.d.Log.Error("addons push: batch", "addon", id, "err", err)
			return
		}
		sendCtx, cancel := context.WithTimeout(ctx, addons.AdapterTimeout)
		err = s.d.Addons.PushEvents(sendCtx, tgt.installed, body)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			pause = nextPause(pause, tune)
			s.d.Log.Warn("addons push: the addon did not take its events, trying again", "addon", id, "after", pause, "err", err)
			if !s.pushWait(ctx, id, pause) {
				return
			}
			continue
		}
		// Only now: a batch the addon did not take is still ahead of the cursor.
		if err := s.d.Store.Q.AckAddonEvents(ctx, db.AckAddonEventsParams{AddonID: id, LastID: rows[len(rows)-1].ID, UpdatedAt: s.d.Now().Unix()}); err != nil {
			s.d.Log.Warn("addons push: cursor not moved, the batch is sent again", "addon", id, "err", err)
			pause = nextPause(pause, tune)
			if !s.pushWait(ctx, id, pause) {
				return
			}
			continue
		}
		pause = 0
	}
}

// nextPause doubles the pause from the first to the longest.
func nextPause(pause time.Duration, tune pushTune) time.Duration {
	if pause == 0 {
		return tune.first
	}
	return min(2*pause, tune.max)
}

// pushWait sleeps for d in short slices and says whether the worker goes on: not when ctx
// ended or the addon stopped being a target (it is not running, or no longer asks), so a
// pause of five minutes does not outlive the addon.
func (s *Server) pushWait(ctx context.Context, id string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			return true
		}
		t := time.NewTimer(min(left, time.Second))
		select {
		case <-ctx.Done():
			t.Stop()
			return false
		case <-t.C:
		}
		if _, ok := s.pushTarget(ctx, id); !ok {
			return false
		}
	}
}
