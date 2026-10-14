package panelapi

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"mikan/internal/panel/events"
	"mikan/internal/panel/store/db"
)

// The feed of events (permission events), read by pulling: an addon with no HTTP server of
// its own (a bot on a framework) reads it the same way as the rest. GET /events gives what
// follows the addon's cursor (or ?after=), waiting up to ?wait= seconds for something new;
// POST /events/ack moves the cursor. Delivery is at least once: an addon dedupes by id.
//
// An addon that serves HTTP may ask for push instead ("push": true in its /v2/info): the panel
// then sends it the same events (push.go) with the same cursor, and the two ways are never
// both open at once: while it is pushed to, GET /events and /events/ack answer 409 push_delivery,
// so an addon that is switched from one to the other does not read an event twice from two
// places or move the cursor under the worker.

const maxEvents = 500

func (s *Server) handleEvents() {
	s.Handle(http.MethodGet, "/events", "events", s.eventsList)
	s.Handle(http.MethodPost, "/events/ack", "events", s.eventsAck)
}

// pushedTo: the addon is sent its events by the panel, so it does not pull them.
func (s *Server) pushedTo(w http.ResponseWriter, r *http.Request, c Caller) bool {
	// The question goes to the addon the first time per build; a slow one must not hold the
	// pull of one that is not pushed to.
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if _, _, push := s.d.Addons.Pushes(ctx, c.ID); !push {
		return false
	}
	Fail(w, http.StatusConflict, "push_delivery", "the panel pushes events to this addon (POST /v2/events): it does not pull them")
	return true
}

func (s *Server) eventsList(w http.ResponseWriter, r *http.Request) {
	c, _ := CallerOf(r.Context())
	if s.pushedTo(w, r, c) {
		return
	}
	q := r.URL.Query()
	num := func(name string, def, lo, hi int64) (int64, bool) {
		n, ok := intParam(q.Get(name), def, lo, hi)
		if !ok {
			Fail(w, http.StatusBadRequest, "bad_request", name+" is "+strconv.FormatInt(lo, 10)+" to "+strconv.FormatInt(hi, 10))
		}
		return n, ok
	}
	limit, ok := num("limit", 100, 1, maxEvents)
	if !ok {
		return
	}
	wait, ok := num("wait", 0, 0, maxPollWait)
	if !ok {
		return
	}
	var types []string
	if t := q.Get("types"); t != "" {
		types = strings.Split(t, ",")
	}
	var after int64
	if a := q.Get("after"); a != "" {
		if after, ok = intParam(a, 0, 0, math.MaxInt64); !ok {
			Fail(w, http.StatusBadRequest, "bad_request", "after is an event id")
			return
		}
	} else {
		cur, err := s.d.Store.Q.GetAddonCursor(r.Context(), c.ID)
		if err != nil {
			s.internal(w, "events cursor", err)
			return
		}
		after = cur
	}
	// The bot's messages (bot.*) carry whole subscriptions and payments: the holder's alone.
	holder := s.holds(r.Context(), c)
	rows, err := longPoll(r.Context(), s.d.Hub, time.Duration(wait)*time.Second, func() ([]db.AddonEvent, error) {
		return s.d.Store.Q.ListAddonEvents(r.Context(), db.ListAddonEventsParams{After: after, Types: types, BotToo: holder, Lim: pageSize(limit, maxEvents)})
	})
	if r.Context().Err() != nil {
		return
	}
	if err != nil {
		s.internal(w, "events", err)
		return
	}
	Write(w, http.StatusOK, struct {
		Events []events.Event `json:"events"`
	}{Events: s.feedOf(rows, holder)})
}

// feedOf is the rows as the addon gets them, by pull or by push. The holder of the bot is
// about to write to the accounts they name, so they count as talking to it.
func (s *Server) feedOf(rows []db.AddonEvent, holder bool) []events.Event {
	out := make([]events.Event, 0, len(rows))
	for _, e := range rows {
		if holder {
			s.talk.mark(e.TgID.Int64)
		}
		out = append(out, events.Event{ID: e.ID, Type: e.Type, At: time.Unix(e.CreatedAt, 0).UTC(),
			UserID: e.UserID.Int64, TgID: e.TgID.Int64, Data: json.RawMessage(e.Data)})
	}
	return out
}

func (s *Server) eventsAck(w http.ResponseWriter, r *http.Request) {
	c, _ := CallerOf(r.Context())
	if s.pushedTo(w, r, c) {
		return
	}
	var in struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.ID < 1 {
		Fail(w, http.StatusBadRequest, "bad_request", `the body is {"id": <the last event handled>}`)
		return
	}
	// The cursor only moves forward: a late ack of an older event does not replay the rest.
	if err := s.d.Store.Q.AckAddonEvents(r.Context(), db.AckAddonEventsParams{AddonID: c.ID, LastID: in.ID, UpdatedAt: s.d.Now().Unix()}); err != nil {
		s.internal(w, "events ack", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
