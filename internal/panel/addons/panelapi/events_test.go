package panelapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/events"
	"mikan/internal/panel/store/db"
)

type feed struct {
	Events []events.Event `json:"events"`
}

// An addon reads the feed from its cursor, filtered by type, waits for what comes next and
// moves its cursor only forward; another addon has a cursor of its own.
func TestEvents(t *testing.T) {
	s, _ := kvWorld(t)
	hub := events.NewHub()
	s.d.Hub = hub
	ctx := context.Background()
	emit := func(typ string, user int64) {
		t.Helper()
		err := s.d.Store.Tx(ctx, func(q *db.Queries) error {
			return events.Emit(ctx, q, time.Unix(1_800_000_000, 0), typ, user, 555, map[string]int64{"user": user}, "")
		})
		if err != nil {
			t.Fatal(err)
		}
		hub.Kick()
	}
	read := func(token, query string) (int, feed) {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:41990/panel/v2/events"+query, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		var f feed
		_ = json.Unmarshal(w.Body.Bytes(), &f)
		return w.Code, f
	}
	ack := func(token string, id int64) int {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:41990/panel/v2/events/ack", strings.NewReader(`{"id":`+itoa64(id)+`}`))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w.Code
	}

	emit(events.PaymentPaid, 1)
	emit(events.PaymentRefunded, 1)
	emit(events.PaymentPaid, 2)

	// telegram's grant has no events
	if code, _ := read(botToken, ""); code != http.StatusForbidden {
		t.Fatalf("without the permission: %d", code)
	}
	_ = s.d.Store.Q.UpsertAddonGrant(ctx, db.UpsertAddonGrantParams{AddonID: "fresh", Version: "1", Permissions: `["events","kv"]`, ApprovedAt: 1})

	_, f := read(newToken, "")
	if len(f.Events) != 3 || f.Events[0].Type != events.PaymentPaid || f.Events[0].TgID != 555 || string(f.Events[2].Data) != `{"user":2}` {
		t.Fatalf("feed: %+v", f)
	}
	_, f = read(newToken, "?types=payment.refunded")
	if len(f.Events) != 1 || f.Events[0].Type != events.PaymentRefunded {
		t.Fatalf("filtered: %+v", f)
	}
	if code := ack(newToken, f.Events[0].ID); code != http.StatusNoContent {
		t.Fatalf("ack: %d", code)
	}
	_, f = read(newToken, "")
	if len(f.Events) != 1 || f.Events[0].UserID != 2 {
		t.Fatalf("after the ack: %+v", f)
	}
	last := f.Events[0].ID
	// an older ack does not move it back, one past the end does not skip what comes
	ack(newToken, 1)
	ack(newToken, last+1000)
	_, f = read(newToken, "")
	if len(f.Events) != 0 {
		t.Fatalf("after acking the last: %+v", f)
	}

	// a long poll returns as soon as something comes
	done := make(chan feed)
	go func() {
		_, f := read(newToken, "?wait=20")
		done <- f
	}()
	time.Sleep(300 * time.Millisecond)
	start := time.Now()
	emit(events.PaymentPaid, 3)
	select {
	case f := <-done:
		if len(f.Events) != 1 || f.Events[0].UserID != 3 || f.Events[0].ID <= last {
			t.Fatalf("woken with %+v", f)
		}
		if time.Since(start) > 5*time.Second {
			t.Fatal("the long poll did not wake on the event")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the long poll did not return")
	}
	// an empty wait answers when it runs out
	start = time.Now()
	ack(newToken, last+1000)
	if code, f := read(newToken, "?wait=1"); code != 200 || len(f.Events) != 0 || time.Since(start) < time.Second {
		t.Fatalf("an empty wait: %d %+v after %v", code, f, time.Since(start))
	}
	for _, q := range []string{"?wait=99", "?limit=0", "?after=-1"} {
		if code, _ := read(newToken, q); code != http.StatusBadRequest {
			t.Fatalf("%s: %d", q, code)
		}
	}
}

// A second emit of the same thing (a retried transaction, a scheduler run twice) is no event.
func TestEmitDedupe(t *testing.T) {
	s, _ := kvWorld(t)
	ctx := context.Background()
	for range 2 {
		if err := s.d.Store.Tx(ctx, func(q *db.Queries) error {
			return events.Emit(ctx, q, time.Unix(1, 0), "user.expiring", 7, 0, map[string]int{"days": 3}, "user.expiring:7:3d:1")
		}); err != nil {
			t.Fatal(err)
		}
	}
	evs, err := s.d.Store.Q.ListAddonEvents(ctx, db.ListAddonEventsParams{Types: []string{}, Lim: 10})
	if err != nil || len(evs) != 1 {
		t.Fatalf("%d events, %v", len(evs), err)
	}
	// Retention drops a week-old event.
	if n, err := events.Prune(ctx, s.d.Store.Q, time.Unix(1, 0).Add(events.Retention+time.Second)); err != nil || n != 1 {
		t.Fatalf("pruned %d, %v", n, err)
	}
}

func itoa64(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
