package webhooks

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"mikan/internal/panel/events"
	"mikan/internal/panel/events/feed"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/store/storetest"
)

// world is a service over a real database, with quick pauses.
type world struct {
	t   *testing.T
	st  *store.Store
	hub *events.Hub
	s   *Service
}

func newWorld(t *testing.T) *world {
	t.Helper()
	st, err := storetest.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	hub := events.NewHub()
	s := newService(Deps{Store: st, Hub: hub, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: time.Now, Version: "test"},
		tune{first: 30 * time.Millisecond, max: 120 * time.Millisecond, scan: 20 * time.Millisecond, timeout: 5 * time.Second})
	return &world{t: t, st: st, hub: hub, s: s}
}

// emit writes an event to the feed, the way the panel does, and wakes the workers; it gives
// the event's id.
func (w *world) emit(typ string, user int64) int64 {
	w.t.Helper()
	ctx := context.Background()
	err := w.st.Tx(ctx, func(q *db.Queries) error {
		return feed.Emit(ctx, q, time.Unix(1_800_000_000, 0), typ, user, 555, map[string]int64{"user": user}, "")
	})
	if err != nil {
		w.t.Fatal(err)
	}
	w.hub.Kick()
	return w.feedEnd()
}

func (w *world) feedEnd() int64 {
	w.t.Helper()
	var id int64
	if err := w.st.DB.QueryRow("SELECT COALESCE(max(id), 0) FROM addon_events").Scan(&id); err != nil {
		w.t.Fatal(err)
	}
	return id
}

// run starts the delivery; it is stopped, and waited for, when the test ends.
func (w *world) run() {
	w.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); w.s.Run(ctx) }()
	w.t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			w.t.Error("the delivery did not stop")
		}
	})
}

// endpoint makes an endpoint that goes to r and may reach local addresses (a test server is
// one), told of the events named, all of them if none.
func (w *world) endpoint(r *receiver, typs ...string) db.WebhookEndpoint {
	w.t.Helper()
	return w.endpointIn(Input{Name: "test", URL: r.srv.URL + "/hook", Events: typs, Enabled: true, AllowLocal: true})
}

func (w *world) endpointIn(in Input) db.WebhookEndpoint {
	w.t.Helper()
	ep, err := w.s.Create(context.Background(), in)
	if err != nil {
		w.t.Fatal(err)
	}
	return ep
}

func (w *world) get(id int64) db.WebhookEndpoint {
	w.t.Helper()
	ep, err := w.st.Q.GetWebhook(context.Background(), id)
	if err != nil {
		w.t.Fatal(err)
	}
	return ep
}

// log is an endpoint's log, newest first.
func (w *world) log(id int64) []db.WebhookDelivery {
	w.t.Helper()
	rows, err := w.s.Deliveries(context.Background(), id, 0, "", 1000)
	if err != nil {
		w.t.Fatal(err)
	}
	return rows
}

// row is the log's row of one event.
func (w *world) row(endpoint, event int64) (db.WebhookDelivery, bool) {
	w.t.Helper()
	for _, d := range w.log(endpoint) {
		if d.EventID.Valid && d.EventID.Int64 == event {
			return d, true
		}
	}
	return db.WebhookDelivery{}, false
}

// received is one request a receiver took.
type received struct {
	at     time.Time
	method string
	header http.Header
	body   []byte
}

// event is what the request carried.
func (r received) event(t *testing.T) events.Event {
	t.Helper()
	var ev events.Event
	if err := json.Unmarshal(r.body, &ev); err != nil {
		t.Fatalf("the body is not an event: %s (%v)", r.body, err)
	}
	return ev
}

// receiver is a server that takes webhooks: it keeps what it was sent and answers as told.
type receiver struct {
	srv *httptest.Server

	mu      sync.Mutex
	answers []int // the statuses of the next requests; after them 204
	got     []received
}

func newReceiver(t *testing.T) *receiver {
	t.Helper()
	r := &receiver{}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.got = append(r.got, received{at: time.Now(), method: req.Method, header: req.Header.Clone(), body: body})
		status := http.StatusNoContent
		if len(r.answers) > 0 {
			status, r.answers = r.answers[0], r.answers[1:]
		}
		r.mu.Unlock()
		if status/100 == 3 {
			w.Header().Set("Location", "/elsewhere")
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

// answer sets the statuses of the next requests.
func (r *receiver) answer(statuses ...int) {
	r.mu.Lock()
	r.answers = append(r.answers, statuses...)
	r.mu.Unlock()
}

// forever answers every request with status from now on.
func (r *receiver) forever(status int) {
	r.mu.Lock()
	r.answers = slices.Repeat([]int{status}, 100000)
	r.mu.Unlock()
}

func (r *receiver) posts() []received {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.got)
}

// ids are the events' ids, in the order the receiver was sent them.
func (r *receiver) ids(t *testing.T) (ids []int64) {
	t.Helper()
	for _, p := range r.posts() {
		ids = append(ids, p.event(t).ID)
	}
	return ids
}

// eventually waits for ok, a polling test's way of watching the workers.
func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
