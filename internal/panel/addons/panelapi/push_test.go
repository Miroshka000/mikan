package panelapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"mikan/internal/panel/addons"
	"mikan/internal/panel/events"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

// hook is an addon that serves HTTP: its /v2/info says whether it wants its events pushed
// and which, and POST /v2/events keeps what it was sent and answers as it is told to.
type hook struct {
	id  string
	srv *httptest.Server

	mu      sync.Mutex
	push    bool
	types   []string
	answers []int // statuses of the next posts; then 204
	posts   []post
	hang    bool // /v2/info never answers
	infos   int  // how many times it was asked
}

type post struct {
	at    time.Time
	auth  string
	ids   []int64
	types []string
}

func newHook(t *testing.T, id string, push bool, types ...string) *hook {
	h := &hook{id: id, push: push, types: types}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v2/info":
			h.mu.Lock()
			info := addons.InfoV2{ID: id, Protocol: addons.Protocol2, Push: h.push, Events: h.types}
			h.infos++
			hang := h.hang
			h.mu.Unlock()
			if hang {
				select {
				case <-r.Context().Done():
				case <-time.After(15 * time.Second):
				}
				return
			}
			_ = json.NewEncoder(w).Encode(info)
		case "POST /v2/events":
			var in struct {
				Events []events.Event `json:"events"`
			}
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			p := post{at: time.Now(), auth: r.Header.Get("Authorization")}
			for _, e := range in.Events {
				p.ids, p.types = append(p.ids, e.ID), append(p.types, e.Type)
			}
			h.mu.Lock()
			h.posts = append(h.posts, p)
			status := http.StatusNoContent
			if len(h.answers) > 0 {
				status, h.answers = h.answers[0], h.answers[1:]
			}
			h.mu.Unlock()
			if status/100 == 3 {
				w.Header().Set("Location", "/elsewhere")
			}
			w.WriteHeader(status)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *hook) failNext(statuses ...int) {
	h.mu.Lock()
	h.answers = append(h.answers, statuses...)
	h.mu.Unlock()
}

func (h *hook) got() []post {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.posts)
}

func (h *hook) delivered() (ids []int64) {
	for _, p := range h.got() {
		ids = append(ids, p.ids...)
	}
	return ids
}

func (h *hook) token() string { return "tok-" + h.id }

func (h *hook) panelToken() string {
	return (h.id + strings.Repeat("-", 48))[:48]
}

type pushed struct {
	s    *Server
	dir  string
	st   addons.State
	hub  *events.Hub
	hook map[string]*hook
}

// pushWorld is a server over a real database where every hook is a running addon of protocol
// 2 that serves HTTP, with the permissions perms[id] approved, and quick pauses.
func pushWorld(t *testing.T, perms map[string]string, hooks ...*hook) *pushed {
	t.Helper()
	st := addons.State{Adapters: map[string]addons.Installed{}}
	g := grants{}
	byID := map[string]*hook{}
	for _, h := range hooks {
		st.Adapters[h.id] = addons.Installed{Protocol: addons.Protocol2, Kind: addons.KindGeneric, Mode: addons.ModeHTTP, Version: "1.0.0",
			Listen: strings.TrimPrefix(h.srv.URL, "http://"), Token: h.token(), PanelToken: h.panelToken(), Status: "running"}
		g[h.id] = db.AddonGrant{AddonID: h.id, Version: "1.0.0", Permissions: perms[h.id]}
		byID[h.id] = h
	}
	s, dir := world(t, st, g)
	p := &pushed{s: s, dir: dir, st: st, hub: events.NewHub(), hook: byID}
	s.d.Hub = p.hub
	s.push = pushTune{first: 30 * time.Millisecond, max: 120 * time.Millisecond, scan: 20 * time.Millisecond}
	return p
}

func (p *pushed) emit(t *testing.T, typ string, user int64) {
	t.Helper()
	ctx := context.Background()
	err := p.s.d.Store.Tx(ctx, func(q *db.Queries) error {
		return events.Emit(ctx, q, time.Unix(1_800_000_000, 0), typ, user, 555, map[string]int64{"user": user}, "")
	})
	if err != nil {
		t.Fatal(err)
	}
	p.hub.Kick()
}

// run starts the push worker; it is stopped and waited for when the test ends.
func (p *pushed) run(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); p.s.RunPush(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			t.Error("the push worker did not stop")
		}
	})
}

func (p *pushed) setStatus(t *testing.T, id, status string) {
	t.Helper()
	a := p.st.Adapters[id]
	a.Status = status
	p.st.Adapters[id] = a
	writeState(t, p.dir, p.st)
}

func (p *pushed) cursor(t *testing.T, id string) int64 {
	t.Helper()
	n, err := p.s.d.Store.Q.GetAddonCursor(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

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

func (p *pushed) pull(t *testing.T, h *hook, path string) (int, string, feed) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:41990/panel/v2/events"+path, nil)
	r.Header.Set("Authorization", "Bearer "+h.panelToken())
	w := httptest.NewRecorder()
	p.s.ServeHTTP(w, r)
	var e Error
	_ = json.Unmarshal(w.Body.Bytes(), &e)
	var f feed
	_ = json.Unmarshal(w.Body.Bytes(), &f)
	return w.Code, e.Code, f
}

// The panel sends the feed to an addon that asks for it, in batches of at most 100 and in id
// order, with the addon's own token; what comes later comes at once.
func TestPushDeliversInBatches(t *testing.T) {
	h := newHook(t, "hook", true)
	p := pushWorld(t, map[string]string{"hook": `["events"]`}, h)
	for i := range 250 {
		p.emit(t, events.PaymentPaid, int64(i+1))
	}
	p.run(t)
	eventually(t, "250 events pushed", func() bool { return len(h.delivered()) == 250 })
	var sizes []int
	for _, b := range h.got() {
		sizes = append(sizes, len(b.ids))
		if b.auth != "Bearer "+h.token() {
			t.Fatalf("the batch came with %q, want the addon's own token", b.auth)
		}
	}
	if !slices.Equal(sizes, []int{100, 100, 50}) {
		t.Fatalf("batches %v", sizes)
	}
	ids := h.delivered()
	if !slices.IsSorted(ids) || len(slices.Compact(slices.Clone(ids))) != 250 {
		t.Fatalf("not in id order, once each: %v", ids)
	}
	eventually(t, "the cursor at the last event", func() bool { return p.cursor(t, "hook") == ids[len(ids)-1] })

	// Something new goes out as soon as it is there.
	p.emit(t, events.PaymentRefunded, 300)
	eventually(t, "the new event pushed", func() bool { return len(h.delivered()) == 251 })
	if last := h.got()[len(h.got())-1]; len(last.ids) != 1 || last.types[0] != events.PaymentRefunded {
		t.Fatalf("last batch %+v", last)
	}
}

// A batch the addon does not take (any answer but 2xx, a redirect among them) is sent again,
// whole, after a pause that doubles to a ceiling; the cursor stays where it was until a 2xx.
func TestPushRetriesUntil2xx(t *testing.T) {
	h := newHook(t, "hook", true)
	p := pushWorld(t, map[string]string{"hook": `["events"]`}, h)
	for i := range 3 {
		p.emit(t, events.PaymentPaid, int64(i+1))
	}
	h.failNext(http.StatusInternalServerError, http.StatusNotFound, http.StatusFound, http.StatusServiceUnavailable)
	p.run(t)
	eventually(t, "five posts", func() bool { return len(h.got()) >= 5 })
	posts := h.got()
	for i, b := range posts[:5] {
		if !slices.Equal(b.ids, posts[0].ids) || len(b.ids) != 3 {
			t.Fatalf("post %d carried %v, want the same three events again", i, b.ids)
		}
	}
	// 30 ms, 60 ms, then the ceiling of 120 ms.
	for i, want := range []time.Duration{30, 60, 120, 120} {
		gap := posts[i+1].at.Sub(posts[i].at)
		if gap < want*time.Millisecond-5*time.Millisecond {
			t.Fatalf("pause %d was %v, want at least %v", i, gap, want*time.Millisecond)
		}
	}
	eventually(t, "the cursor after the 2xx", func() bool { return p.cursor(t, "hook") == posts[4].ids[2] })
	if n := len(h.got()); n != 5 {
		t.Fatalf("%d posts: a taken batch was sent again", n)
	}
}

// While the addon is not running the worker stops, whatever pause it was in, and the events
// wait; when it runs again a new worker delivers them from the cursor without the old pause.
func TestPushStopsWhileNotRunning(t *testing.T) {
	h := newHook(t, "hook", true)
	p := pushWorld(t, map[string]string{"hook": `["events"]`}, h)
	p.s.push = pushTune{first: time.Hour, max: time.Hour, scan: 20 * time.Millisecond}
	p.emit(t, events.PaymentPaid, 1)
	h.failNext(http.StatusInternalServerError)
	p.run(t)
	eventually(t, "the first post", func() bool { return len(h.got()) == 1 })
	// The worker is now in a pause of an hour. The addon stops.
	p.setStatus(t, "hook", "stopped")
	time.Sleep(2500 * time.Millisecond)
	p.emit(t, events.PaymentPaid, 2)
	time.Sleep(1500 * time.Millisecond)
	if n := len(h.got()); n != 1 {
		t.Fatalf("%d posts while the addon was stopped", n)
	}
	if p.cursor(t, "hook") != 0 {
		t.Fatal("the cursor moved while nothing was taken")
	}
	p.setStatus(t, "hook", "running")
	eventually(t, "both events once it runs again", func() bool { return len(h.delivered()) >= 3 })
	posts := h.got()
	if last := posts[len(posts)-1]; len(last.ids) != 2 {
		t.Fatalf("after the restart %+v", posts)
	}
}

// Only an addon that has the events permission, serves HTTP and asks for it is sent anything;
// the others pull as before, and a pushed one is told so when it pulls.
func TestPushOnlyWhereAskedAndAllowed(t *testing.T) {
	pushing := newHook(t, "pushing", true)
	noPerm := newHook(t, "noperm", true)
	pulling := newHook(t, "pulling", false)
	p := pushWorld(t, map[string]string{"pushing": `["events"]`, "noperm": `["kv"]`, "pulling": `["events"]`}, pushing, noPerm, pulling)
	p.emit(t, events.PaymentPaid, 1)
	p.run(t)
	eventually(t, "the pushed addon gets its event", func() bool { return len(pushing.delivered()) == 1 })
	time.Sleep(1200 * time.Millisecond)
	if n := len(noPerm.got()); n != 0 {
		t.Fatalf("an addon without the permission was sent %d batches", n)
	}
	if n := len(pulling.got()); n != 0 {
		t.Fatalf("an addon that did not ask was sent %d batches", n)
	}

	// The one that did not ask pulls, and its cursor is its own.
	code, _, f := p.pull(t, pulling, "")
	if code != http.StatusOK || len(f.Events) != 1 {
		t.Fatalf("pull of an addon that is not pushed to: %d %+v", code, f)
	}
	// The pushed one does not: pull and ack are refused, so the two never move one cursor.
	if code, c, _ := p.pull(t, pushing, ""); code != http.StatusConflict || c != "push_delivery" {
		t.Fatalf("pull of a pushed addon: %d %s", code, c)
	}
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:41990/panel/v2/events/ack", strings.NewReader(`{"id":1}`))
	r.Header.Set("Authorization", "Bearer "+pushing.panelToken())
	w := httptest.NewRecorder()
	p.s.ServeHTTP(w, r)
	if w.Code != http.StatusConflict {
		t.Fatalf("ack of a pushed addon: %d", w.Code)
	}
	// One without the permission is refused as ever.
	if code, c, _ := p.pull(t, noPerm, ""); code != http.StatusForbidden || c != "forbidden" {
		t.Fatalf("pull without the permission: %d %s", code, c)
	}
	// And a token that is not an addon's gets nothing.
	r = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:41990/panel/v2/events", nil)
	w = httptest.NewRecorder()
	p.s.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("pull without a token: %d", w.Code)
	}
}

// Pushed events are filtered as pulled ones are: the types the addon named, and the bot's
// own messages (bot.*) only for the addon that holds the bot.
func TestPushFiltersLikePull(t *testing.T) {
	narrow := newHook(t, "narrow", true, events.PaymentRefunded, events.BotPaid)
	holder := newHook(t, "holder", true)
	plain := newHook(t, "plain", true)
	p := pushWorld(t, map[string]string{"narrow": `["events"]`, "holder": `["events","bot"]`, "plain": `["events"]`}, narrow, holder, plain)
	if err := settings.Set(context.Background(), p.s.d.Settings, SlotKey, "holder"); err != nil {
		t.Fatal(err)
	}
	p.emit(t, events.PaymentPaid, 1)
	p.emit(t, events.BotPaid, 1)
	p.emit(t, events.PaymentRefunded, 1)
	p.run(t)
	eventually(t, "the holder gets all three", func() bool { return len(holder.delivered()) == 3 })
	eventually(t, "the others get theirs", func() bool { return len(plain.delivered()) == 2 && len(narrow.delivered()) == 1 })
	time.Sleep(500 * time.Millisecond)
	types := func(h *hook) (out []string) {
		for _, b := range h.got() {
			out = append(out, b.types...)
		}
		return out
	}
	if got := types(holder); !slices.Equal(got, []string{events.PaymentPaid, events.BotPaid, events.PaymentRefunded}) {
		t.Fatalf("the holder was sent %v", got)
	}
	if got := types(plain); !slices.Equal(got, []string{events.PaymentPaid, events.PaymentRefunded}) {
		t.Fatalf("an addon that does not hold the bot was sent %v", got)
	}
	// It asked for bot.paid, which is not its to have: only the refund comes.
	if got := types(narrow); !slices.Equal(got, []string{events.PaymentRefunded}) {
		t.Fatalf("the narrow addon was sent %v", got)
	}
	// Each has its own cursor; the narrow one's is at what it was sent.
	last := plain.delivered()[1]
	eventually(t, "cursors", func() bool { return p.cursor(t, "plain") == last && p.cursor(t, "narrow") == last })
	if c := p.cursor(t, "holder"); c != last {
		t.Fatalf("the holder's cursor %d, want %d", c, last)
	}
}

// A name in the addon's list that is not an event type matches no event: the addon is never
// sent more than it named, and one that named nothing real is sent nothing, not everything.
func TestPushSlipInTypesDoesNotWidenTheFeed(t *testing.T) {
	h := newHook(t, "slip", true, events.PaymentRefunded, "Bad Type")
	none := newHook(t, "none", true, "Bad Type")
	p := pushWorld(t, map[string]string{"slip": `["events"]`, "none": `["events"]`}, h, none)
	p.emit(t, events.PaymentPaid, 1)
	p.emit(t, events.PaymentRefunded, 1)
	p.run(t)
	eventually(t, "the refund is sent", func() bool { return len(h.delivered()) >= 1 })
	time.Sleep(500 * time.Millisecond)
	for _, b := range h.got() {
		if !slices.Equal(b.types, []string{events.PaymentRefunded}) {
			t.Fatalf("an addon that named one valid type was sent %v", b.types)
		}
	}
	if got := none.delivered(); len(got) != 0 {
		t.Fatalf("an addon that named no event type was sent %v", got)
	}
}

// An addon whose /v2/info does not answer is asked once, then left alone for a while: its pulls
// are not each held for the wait, and the others are sent theirs meanwhile.
func TestPushHungInfoIsNotAskedAgainAtOnce(t *testing.T) {
	hung := newHook(t, "hung", true)
	hung.hang = true
	good := newHook(t, "good", true)
	p := pushWorld(t, map[string]string{"hung": `["events"]`, "good": `["events"]`}, hung, good)
	p.emit(t, events.PaymentPaid, 1)

	if code, _, _ := p.pull(t, hung, ""); code != http.StatusOK {
		t.Fatalf("pull of an addon whose info hangs: %d", code)
	}
	begin := time.Now()
	if code, _, _ := p.pull(t, hung, ""); code != http.StatusOK || time.Since(begin) > time.Second {
		t.Fatalf("second pull took %v (%d): it asked the hung addon again", time.Since(begin), code)
	}
	hung.mu.Lock()
	asked := hung.infos
	hung.mu.Unlock()
	if asked != 1 {
		t.Fatalf("/v2/info of the hung addon was asked %d times", asked)
	}

	p.run(t)
	eventually(t, "the other addon is sent its event", func() bool { return len(good.delivered()) == 1 })
}
