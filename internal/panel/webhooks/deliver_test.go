package webhooks

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"mikan/internal/pace"
	"mikan/internal/panel/events"
	"mikan/internal/panel/store/db"
)

// Events go to an endpoint one to a request, in id order, each signed by the endpoint's secret
// in the envelope the addons' feed has; a receiver written from the documentation takes them.
func TestDeliversEachEventSigned(t *testing.T) {
	w := newWorld(t)
	r := newReceiver(t)
	ep := w.endpoint(r)
	paid := w.emit(events.PaymentPaid, 7)
	made := w.emit(events.UserCreated, 8)
	down := w.emit(events.NodeDown, 0)
	w.run()
	eventually(t, "three requests", func() bool { return len(r.posts()) == 3 })

	for i, want := range []struct {
		id   int64
		typ  string
		user int64
	}{{paid, events.PaymentPaid, 7}, {made, events.UserCreated, 8}, {down, events.NodeDown, 0}} {
		p := r.posts()[i]
		if p.method != http.MethodPost || p.header.Get("Content-Type") != "application/json" || p.header.Get("User-Agent") != "mikan-webhooks/test" {
			t.Errorf("request %d: %s %q %q", i, p.method, p.header.Get("Content-Type"), p.header.Get("User-Agent"))
		}
		if p.header.Get("X-Mikan-Event") != want.typ || p.header.Get("X-Mikan-Event-Id") != idText(want.id) {
			t.Errorf("request %d names %q #%q", i, p.header.Get("X-Mikan-Event"), p.header.Get("X-Mikan-Event-Id"))
		}
		if err := verify(ep.Secret, p.header, p.body, time.Now()); err != nil {
			t.Errorf("request %d: a receiver refuses it: %v", i, err)
		}
		var raw map[string]any
		if err := json.Unmarshal(p.body, &raw); err != nil {
			t.Fatal(err)
		}
		ev := p.event(t)
		if ev.ID != want.id || ev.Type != want.typ || ev.UserID != want.user || ev.TgID != 555 || !ev.At.Equal(time.Unix(1_800_000_000, 0)) {
			t.Errorf("request %d carried %+v", i, ev)
		}
		if raw["at"] != "2027-01-15T08:00:00Z" {
			t.Errorf("request %d: at %v", i, raw["at"])
		}
		if data, ok := raw["data"].(map[string]any); !ok || data["user"] != float64(want.user) {
			t.Errorf("request %d: data in %s", i, p.body)
		}
	}
	// Each was taken, so the log has the three and the cursor is at the last.
	eventually(t, "the cursor at the last event", func() bool { return w.get(ep.ID).LastID == down })
	log := w.log(ep.ID)
	if len(log) != 3 {
		t.Fatalf("%d rows in the log", len(log))
	}
	for _, d := range log {
		if d.Status != "delivered" || d.Attempts != 1 || d.ResponseCode != 204 || d.Error != "" || !d.EventID.Valid {
			t.Errorf("row %+v", d)
		}
	}
}

func idText(id int64) string { b, _ := json.Marshal(id); return string(b) }

// An endpoint starts at the end of the feed: the events of before it was made are not sent.
func TestNewEndpointIsNotToldWhatHappenedBefore(t *testing.T) {
	w := newWorld(t)
	r := newReceiver(t)
	w.emit(events.PaymentPaid, 1)
	w.emit(events.PaymentPaid, 2)
	w.endpoint(r)
	fresh := w.emit(events.PaymentPaid, 3)
	w.run()
	eventually(t, "the new event", func() bool { return len(r.posts()) >= 1 })
	time.Sleep(300 * time.Millisecond)
	if ids := r.ids(t); !slices.Equal(ids, []int64{fresh}) {
		t.Fatalf("sent %v, want only #%d", ids, fresh)
	}
}

// An endpoint is sent the types it chose, all of them when it chose none; each has its own
// cursor, and one that is refused does not hold up another.
func TestEndpointsHaveTheirOwnChoiceAndCursor(t *testing.T) {
	w := newWorld(t)
	everything, refunds, stuck := newReceiver(t), newReceiver(t), newReceiver(t)
	all := w.endpoint(everything)
	narrow := w.endpoint(refunds, events.PaymentRefunded)
	hung := w.endpoint(stuck)
	stuck.forever(http.StatusInternalServerError)
	paid := w.emit(events.PaymentPaid, 1)
	made := w.emit(events.UserCreated, 1)
	refunded := w.emit(events.PaymentRefunded, 1)
	w.run()

	eventually(t, "the endpoint of every event gets all three", func() bool { return len(everything.posts()) == 3 })
	eventually(t, "the narrow one gets its type", func() bool { return len(refunds.posts()) == 1 })
	eventually(t, "the one that is refused is asked again", func() bool { return len(stuck.posts()) >= 4 })
	time.Sleep(200 * time.Millisecond)
	if ids := everything.ids(t); !slices.Equal(ids, []int64{paid, made, refunded}) {
		t.Errorf("the first endpoint got %v", ids)
	}
	if ids := refunds.ids(t); !slices.Equal(ids, []int64{refunded}) {
		t.Errorf("the narrow endpoint got %v", ids)
	}
	for _, id := range stuck.ids(t) {
		if id != paid {
			t.Fatalf("the refused endpoint was sent #%d: only the first event, until it is taken", id)
		}
	}
	eventually(t, "the cursors", func() bool { return w.get(all.ID).LastID == refunded && w.get(narrow.ID).LastID == refunded })
	if w.get(hung.ID).LastID != hung.LastID {
		t.Fatalf("the cursor of an endpoint that took nothing moved to %d", w.get(hung.ID).LastID)
	}
}

// The pause after a failure doubles from a second to five minutes, as the addons' push does.
func TestBackoffIsTheAddonPushs(t *testing.T) {
	b := pace.Backoff{Base: retryFirst, Max: retryMax}
	var got []time.Duration
	for n := range 12 {
		got = append(got, b.Next(n))
	}
	s := time.Second
	want := []time.Duration{s, 2 * s, 4 * s, 8 * s, 16 * s, 32 * s, 64 * s, 128 * s, 256 * s, 300 * s, 300 * s, 300 * s}
	if !slices.Equal(got, want) {
		t.Fatalf("pauses %v, want %v", got, want)
	}
}

// An event the receiver does not take (anything but 2xx, a redirect among them) is sent again,
// whole, after a pause that doubles to a ceiling, and the events behind it wait; the cursor
// stays where it was until a 2xx; the log row of the event counts the attempts and keeps the
// last answer; after the 2xx the rest follow at once.
func TestRetriesWithBackoffUntil2xx(t *testing.T) {
	w := newWorld(t)
	w.s.tune.first, w.s.tune.max = 50*time.Millisecond, 800*time.Millisecond
	r := newReceiver(t)
	ep := w.endpoint(r)
	first := w.emit(events.PaymentPaid, 1)
	second := w.emit(events.PaymentPaid, 2)
	third := w.emit(events.PaymentPaid, 3)
	r.answer(500, 404, 302, 503)
	w.run()
	eventually(t, "five requests", func() bool { return len(r.posts()) >= 5 })
	posts := r.posts()
	for i, p := range posts[:5] {
		if got := p.event(t).ID; got != first {
			t.Fatalf("request %d carried #%d, want #%d again", i, got, first)
		}
	}
	// 50 ms, 100, 200 and 400, doubling.
	for i, want := range []time.Duration{50, 100, 200, 400} {
		if gap := posts[i+1].at.Sub(posts[i].at); gap < want*time.Millisecond-5*time.Millisecond {
			t.Fatalf("pause %d was %v, want at least %v", i, gap, want*time.Millisecond)
		}
	}
	// Each attempt is signed afresh: the same event, a new timestamp, a signature that is good.
	for i, p := range posts[:5] {
		if err := verify(ep.Secret, p.header, p.body, time.Now()); err != nil {
			t.Errorf("attempt %d: %v", i, err)
		}
	}
	eventually(t, "the other two", func() bool { return len(r.posts()) == 7 })
	if ids := r.ids(t); !slices.Equal(ids, []int64{first, first, first, first, first, second, third}) {
		t.Fatalf("sent %v", ids)
	}
	eventually(t, "the cursor at the last", func() bool { return w.get(ep.ID).LastID == third })
	row, _ := w.row(ep.ID, first)
	if row.Attempts != 5 || row.Status != "delivered" || row.ResponseCode != 204 || row.Error != "" {
		t.Errorf("the first event's row %+v", row)
	}
	if row, _ := w.row(ep.ID, second); row.Attempts != 1 {
		t.Errorf("the second event's row %+v", row)
	}
	// After a success the pause starts over: a failure now waits the first pause, not the one
	// the fifth would (the ceiling of 800 ms).
	r.answer(500)
	w.emit(events.PaymentPaid, 4)
	eventually(t, "the failure and its retry", func() bool { return len(r.posts()) >= 9 })
	if gap := r.posts()[8].at.Sub(r.posts()[7].at); gap > 400*time.Millisecond {
		t.Errorf("the pause after a success and a new failure was %v", gap)
	}
}

// What the log shows while an event is being retried, and after: the row is retrying with the
// last answer and the attempts so far; every answer but a 2xx keeps the cursor back.
func TestEveryAnswerButA2xxKeepsTheCursorBack(t *testing.T) {
	w := newWorld(t)
	w.run()
	for status, taken := range map[int]bool{200: true, 201: true, 202: true, 204: true, 299: true,
		301: false, 302: false, 307: false, 308: false, 400: false, 401: false, 403: false, 404: false, 405: false, 410: false, 413: false, 429: false, 500: false, 502: false, 503: false, 504: false} {
		t.Run(idText(int64(status)), func(t *testing.T) {
			r := newReceiver(t)
			ep := w.endpoint(r)
			t.Cleanup(func() { _ = w.s.Delete(context.Background(), ep.ID) })
			id := w.emit(events.PaymentPaid, 1)
			r.forever(status)
			if taken {
				eventually(t, "the cursor moves", func() bool { return w.get(ep.ID).LastID == id })
				row, _ := w.row(ep.ID, id)
				if row.Status != "delivered" || row.ResponseCode != int64(status) || row.Error != "" || row.Attempts != 1 {
					t.Fatalf("row %+v", row)
				}
				return
			}
			eventually(t, "three attempts", func() bool { return len(r.posts()) >= 3 })
			if w.get(ep.ID).LastID != ep.LastID {
				t.Fatalf("an answer of %d moved the cursor", status)
			}
			eventually(t, "the row of the retries", func() bool {
				row, ok := w.row(ep.ID, id)
				return ok && row.Attempts >= 3
			})
			row, _ := w.row(ep.ID, id)
			wantFailure := FailStatus
			if status/100 == 3 {
				wantFailure = FailRedirect
			}
			if row.Status != "retrying" || row.ResponseCode != int64(status) || row.Error != wantFailure {
				t.Fatalf("row %+v", row)
			}
			// The receiver recovers, and the event it was owed goes out and is taken.
			r.mu.Lock()
			r.answers = nil
			r.mu.Unlock()
			eventually(t, "the cursor moves once it takes the event", func() bool { return w.get(ep.ID).LastID == id })
			if row, _ := w.row(ep.ID, id); row.Status != "delivered" || row.ResponseCode != 204 || row.Error != "" {
				t.Fatalf("row after the recovery %+v", row)
			}
		})
	}
}

// An address nothing answers at, or that answers too slowly, is a failure like any other, with
// the reason in the log: the events wait and nothing is lost.
func TestUnreachableReceiverKeepsTheEvents(t *testing.T) {
	w := newWorld(t)
	dead := newReceiver(t)
	ep := w.endpoint(dead)
	dead.srv.Close()
	id := w.emit(events.PaymentPaid, 1)
	w.emit(events.PaymentPaid, 2)
	w.run()
	eventually(t, "a refused attempt", func() bool {
		row, ok := w.row(ep.ID, id)
		return ok && row.Attempts >= 2 && row.Error == FailRefused && row.ResponseCode == 0 && row.Status == "retrying"
	})
	if w.get(ep.ID).LastID != ep.LastID {
		t.Fatal("the cursor moved")
	}
	list, err := w.s.List(context.Background())
	if err != nil || list[0].Waiting != 2 {
		t.Fatalf("waiting %d (%v)", list[0].Waiting, err)
	}
}

// A receiver that takes too long is given up on at the limit, and the log says it timed out.
func TestSlowReceiverTimesOut(t *testing.T) {
	w := newWorld(t)
	w.s = newService(w.s.d, tune{first: 30 * time.Millisecond, max: 120 * time.Millisecond, scan: 20 * time.Millisecond, timeout: 150 * time.Millisecond})
	slow := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-slow }))
	t.Cleanup(func() { close(slow); srv.Close() })
	ep := w.endpointIn(Input{Name: "slow", URL: srv.URL, Enabled: true, AllowLocal: true})
	id := w.emit(events.PaymentPaid, 3)
	w.run()
	eventually(t, "a timeout", func() bool {
		row, ok := w.row(ep.ID, id)
		return ok && row.Attempts >= 2 && row.Error == FailTimeout && row.Status == "retrying" && row.ResponseCode == 0
	})
	if w.get(ep.ID).LastID != ep.LastID {
		t.Fatal("the cursor moved")
	}
}

// Events keep their order across the batches they are read in.
func TestKeepsOrderAcrossBatches(t *testing.T) {
	w := newWorld(t)
	r := newReceiver(t)
	ep := w.endpoint(r)
	var want []int64
	for i := range 250 {
		typ := events.PaymentPaid
		if i%3 == 0 {
			typ = events.UserCreated
		}
		want = append(want, w.emit(typ, int64(i+1)))
	}
	w.run()
	eventually(t, "250 requests", func() bool { return len(r.posts()) == 250 })
	if got := r.ids(t); !slices.Equal(got, want) {
		t.Fatalf("not in id order, once each: %v", got)
	}
	eventually(t, "the cursor at the last", func() bool { return w.get(ep.ID).LastID == want[249] })
	// Something new goes out as soon as it is there.
	last := w.emit(events.PaymentRefunded, 300)
	eventually(t, "the new event", func() bool { return len(r.posts()) == 251 })
	if ids := r.ids(t); ids[250] != last {
		t.Fatalf("last request #%d, want #%d", ids[250], last)
	}
}

// An endpoint that is off is sent nothing and keeps what it is owed, and when it is on again the
// events come, in order, from its cursor.
func TestAnEndpointThatIsOffWaits(t *testing.T) {
	w := newWorld(t)
	r := newReceiver(t)
	ep := w.endpointIn(Input{Name: "n", URL: r.srv.URL, Enabled: false, AllowLocal: true})
	one := w.emit(events.PaymentPaid, 1)
	two := w.emit(events.PaymentPaid, 2)
	w.run()
	time.Sleep(400 * time.Millisecond)
	if n := len(r.posts()); n != 0 {
		t.Fatalf("an endpoint that is off was sent %d events", n)
	}
	on := true
	if _, err := w.s.Update(context.Background(), ep.ID, Patch{Enabled: &on}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "both events once it is on", func() bool { return len(r.posts()) == 2 })
	if ids := r.ids(t); !slices.Equal(ids, []int64{one, two}) {
		t.Fatalf("sent %v", ids)
	}
	// Off again, an event waits for it.
	off := false
	if _, err := w.s.Update(context.Background(), ep.ID, Patch{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	three := w.emit(events.PaymentPaid, 3)
	time.Sleep(400 * time.Millisecond)
	if n := len(r.posts()); n != 2 {
		t.Fatalf("%d requests while it was off", n)
	}
	if _, err := w.s.Update(context.Background(), ep.ID, Patch{Enabled: &on}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the event that waited", func() bool { return len(r.posts()) == 3 })
	if ids := r.ids(t); ids[2] != three {
		t.Fatalf("sent %v", ids)
	}
}

// A worker ends with its endpoint, from any pause it is in, and the endpoint's own events stop
// with it: it does not wait five minutes after it was deleted or turned off.
func TestWorkerGoesWithItsEndpoint(t *testing.T) {
	for name, remove := range map[string]func(w *world, id int64){
		"deleted": func(w *world, id int64) {
			if err := w.s.Delete(context.Background(), id); err != nil {
				t.Fatal(err)
			}
		},
		"turned off": func(w *world, id int64) {
			off := false
			if _, err := w.s.Update(context.Background(), id, Patch{Enabled: &off}); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.s.tune.first, w.s.tune.max = time.Hour, time.Hour
			r := newReceiver(t)
			ep := w.endpoint(r)
			r.forever(http.StatusInternalServerError)
			w.emit(events.PaymentPaid, 1)
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			done := make(chan struct{})
			go func() { defer close(done); w.s.follow(ctx, ep.ID) }()
			eventually(t, "the first attempt", func() bool { return len(r.posts()) == 1 })
			// The worker is in a pause of an hour now.
			remove(w, ep.ID)
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("the worker did not end with its endpoint")
			}
			if n := len(r.posts()); n != 1 {
				t.Fatalf("%d requests", n)
			}
		})
	}
}

// Whoever fixes an endpoint's address does not wait for the pause that was running: the next
// try is made at once, to the new address, and the pauses start over.
func TestFixingTheAddressCutsThePauseShort(t *testing.T) {
	w := newWorld(t)
	w.s.tune.first, w.s.tune.max = time.Hour, time.Hour
	broken, fixed := newReceiver(t), newReceiver(t)
	broken.forever(http.StatusInternalServerError)
	ep := w.endpoint(broken)
	id := w.emit(events.PaymentPaid, 1)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan struct{})
	go func() { defer close(done); w.s.follow(ctx, ep.ID) }()
	t.Cleanup(func() { cancel(); <-done })
	eventually(t, "the first attempt", func() bool { return len(broken.posts()) == 1 })

	url := fixed.srv.URL + "/fixed"
	if _, err := w.s.Update(context.Background(), ep.ID, Patch{URL: &url}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the event at the new address", func() bool { return len(fixed.posts()) == 1 })
	if got := fixed.ids(t); !slices.Equal(got, []int64{id}) {
		t.Fatalf("sent %v", got)
	}
	eventually(t, "the cursor", func() bool { return w.get(ep.ID).LastID == id })
}

// A rotated secret signs the very next delivery; the old one no longer fits it. What the
// receiver refused meanwhile (a 401 while it still holds the old secret) is sent again.
func TestRotatedSecretSignsTheNextDelivery(t *testing.T) {
	w := newWorld(t)
	r := newReceiver(t)
	ep := w.endpoint(r)
	w.run()
	w.emit(events.PaymentPaid, 1)
	eventually(t, "the first", func() bool { return len(r.posts()) == 1 })
	rotated, err := w.s.Rotate(context.Background(), ep.ID)
	if err != nil {
		t.Fatal(err)
	}
	w.emit(events.PaymentPaid, 2)
	eventually(t, "the second", func() bool { return len(r.posts()) == 2 })
	posts := r.posts()
	if verify(ep.Secret, posts[0].header, posts[0].body, time.Now()) != nil || verify(rotated.Secret, posts[0].header, posts[0].body, time.Now()) == nil {
		t.Error("the first delivery is not the old secret's alone")
	}
	if verify(rotated.Secret, posts[1].header, posts[1].body, time.Now()) != nil || verify(ep.Secret, posts[1].header, posts[1].body, time.Now()) == nil {
		t.Error("the second delivery is not the new secret's alone")
	}
}

// Where the panel's server cannot go it does not: an endpoint that does not allow local
// targets and leads to one (a name that came to point at it, a row from before) is refused when
// the connection is made, the receiver sees nothing, and the log says why.
func TestAnAddressTheEndpointMayNotReachIsRefusedAtTheConnection(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	r := newReceiver(t)
	// Saved as a row would be after the address changed hands: the check of the form is bypassed.
	ep, err := w.st.Q.CreateWebhook(ctx, db.CreateWebhookParams{Name: "n", Url: r.srv.URL, Secret: NewSecret(), Events: []string{}, Enabled: true, CreatedAt: time.Now().Unix()})
	if err != nil {
		t.Fatal(err)
	}
	id := w.emit(events.PaymentPaid, 1)
	w.run()
	eventually(t, "a refused attempt", func() bool {
		row, ok := w.row(ep.ID, id)
		return ok && row.Attempts >= 2 && row.Error == FailLocal && row.ResponseCode == 0 && row.Status == "retrying"
	})
	if n := len(r.posts()); n != 0 {
		t.Fatalf("the receiver was sent %d requests", n)
	}
	// A test is refused the same way and says so, and the endpoint is none the worse.
	res, err := w.s.Test(ctx, ep.ID)
	if err != nil || res.OK || res.Failure != FailLocal || res.Status != 0 {
		t.Fatalf("test: %+v %v", res, err)
	}
	if n := len(r.posts()); n != 0 {
		t.Fatalf("the receiver was sent %d requests by a test", n)
	}
}

// The test event goes to the endpoint asked, once, whether it is on or not and whatever
// events it is told of, signed like the rest; its answer is told, the attempt is in the log
// and is not retried, and the endpoint's own events are untouched.
func TestTestEvent(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	r := newReceiver(t)
	ep := w.endpointIn(Input{Name: "n", URL: r.srv.URL + "/hook", Events: []string{events.TrialStarted}, Enabled: false, AllowLocal: true})
	owed := w.emit(events.TrialStarted, 1)

	res, err := w.s.Test(ctx, ep.ID)
	if err != nil || !res.OK || res.Status != 204 || res.Failure != "" || res.Took <= 0 {
		t.Fatalf("test: %+v %v", res, err)
	}
	posts := r.posts()
	if len(posts) != 1 {
		t.Fatalf("%d requests", len(posts))
	}
	p := posts[0]
	ev := p.event(t)
	if p.method != http.MethodPost || ev.Type != "webhook.test" || ev.ID != 0 || p.header.Get("X-Mikan-Event") != "webhook.test" || p.header.Get("X-Mikan-Event-Id") != "0" {
		t.Errorf("the test event: %+v, headers %v", ev, p.header)
	}
	var data struct {
		EndpointID int64  `json:"endpoint_id"`
		Message    string `json:"message"`
	}
	if err := json.Unmarshal(ev.Data, &data); err != nil || data.EndpointID != ep.ID || data.Message == "" {
		t.Errorf("data %s (%v)", ev.Data, err)
	}
	if err := verify(ep.Secret, p.header, p.body, time.Now()); err != nil {
		t.Errorf("a receiver refuses the test event: %v", err)
	}
	if got := w.get(ep.ID); got.LastID != ep.LastID {
		t.Errorf("the test moved the cursor to %d", got.LastID)
	}
	if list, _ := w.s.List(ctx); list[0].Waiting != 1 {
		t.Errorf("%d events waiting, want the one that is owed (#%d)", list[0].Waiting, owed)
	}
	rows := w.log(ep.ID)
	if len(rows) != 1 || rows[0].Type != "webhook.test" || rows[0].EventID.Valid || rows[0].Status != "delivered" || rows[0].ResponseCode != 204 || rows[0].Attempts != 1 {
		t.Fatalf("log %+v", rows)
	}

	// A refusal is told and logged as a failure that is final: nothing sends a test again.
	r.answer(http.StatusInternalServerError)
	res, err = w.s.Test(ctx, ep.ID)
	if err != nil || res.OK || res.Status != 500 || res.Failure != FailStatus {
		t.Fatalf("test of a receiver that refuses: %+v %v", res, err)
	}
	on := true
	if _, err := w.s.Update(ctx, ep.ID, Patch{Enabled: &on}); err != nil {
		t.Fatal(err)
	}
	w.run()
	eventually(t, "the owed event", func() bool { return len(r.posts()) == 3 })
	time.Sleep(300 * time.Millisecond)
	if n := len(r.posts()); n != 3 {
		t.Fatalf("%d requests: a failed test was sent again", n)
	}
	failed := 0
	for _, d := range w.log(ep.ID) {
		if d.Type == "webhook.test" && d.Status == "failed" {
			failed++
			if d.ResponseCode != 500 || d.Error != FailStatus || d.Attempts != 1 {
				t.Errorf("row %+v", d)
			}
		}
	}
	if failed != 1 {
		t.Errorf("%d rows of failed tests", failed)
	}
	if ids := r.ids(t); ids[2] != owed {
		t.Errorf("sent %v, want the owed event last", ids)
	}
	if _, err := w.s.Test(ctx, ep.ID+1000); !errors.Is(err, ErrNotFound) {
		t.Errorf("an endpoint that is not there: %v", err)
	}
}

// A panel that is stopping does not count the request it cut short as the receiver's failure:
// nothing is written to the log and the worker ends at once.
func TestStoppingIsNotAFailure(t *testing.T) {
	w := newWorld(t)
	hold := make(chan struct{})
	var asked sync.WaitGroup
	asked.Add(1)
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		once.Do(asked.Done)
		<-hold
	}))
	t.Cleanup(func() { close(hold); srv.Close() })
	ep := w.endpointIn(Input{Name: "n", URL: srv.URL, Enabled: true, AllowLocal: true})
	w.emit(events.PaymentPaid, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); w.s.follow(ctx, ep.ID) }()
	asked.Wait()
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the worker did not stop with the panel")
	}
	if rows := w.log(ep.ID); len(rows) != 0 {
		t.Fatalf("a stop was logged as a delivery: %+v", rows)
	}
	if w.get(ep.ID).LastID != ep.LastID {
		t.Fatal("the cursor moved")
	}
}

// Without a hub to wake it the worker still finds events, within the second it waits for one.
func TestWorksWithoutAHub(t *testing.T) {
	w := newWorld(t)
	w.s.d.Hub = nil
	r := newReceiver(t)
	w.endpoint(r)
	w.run()
	time.Sleep(100 * time.Millisecond)
	id := w.emit(events.PaymentPaid, 1)
	eventually(t, "the event", func() bool { return slices.Equal(r.ids(t), []int64{id}) })
}

// An endpoint that stops asking for a type is not sent the event of it that was being retried,
// and the row says it will not be tried again.
func TestNarrowingEndsTheRetriesOfAType(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	r := newReceiver(t)
	ep := w.endpoint(r)
	r.forever(http.StatusInternalServerError)
	paid := w.emit(events.PaymentPaid, 1)
	w.run()
	eventually(t, "retries of the payment", func() bool {
		row, ok := w.row(ep.ID, paid)
		return ok && row.Attempts >= 2 && row.Status == "retrying"
	})
	only := []string{events.NodeDown}
	if _, err := w.s.Update(ctx, ep.ID, Patch{Events: &only}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the retrying row of the payment ended", func() bool {
		row, _ := w.row(ep.ID, paid)
		return row.Status == "failed"
	})
	// The endpoint goes on with what it asks for.
	r.mu.Lock()
	r.answers = nil
	r.mu.Unlock()
	down := w.emit(events.NodeDown, 0)
	eventually(t, "the node event", func() bool { return w.get(ep.ID).LastID == down })
	posts := len(r.posts())
	time.Sleep(300 * time.Millisecond)
	if n := len(r.posts()); n != posts {
		t.Fatalf("%d requests after the choice changed: the payment was sent again", n-posts)
	}
	if row, _ := w.row(ep.ID, paid); row.Status != "failed" {
		t.Fatalf("the payment's row %+v", row)
	}
}
