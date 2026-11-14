package webhooks

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/events"
	"mikan/internal/panel/store/db"
)

// An endpoint is made with a secret of its own, in the order of the events' own list and
// without repeats, and it starts where the feed ends: what happened before is not its.
func TestCreate(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.emit(events.PaymentPaid, 1)
	end := w.emit(events.PaymentPaid, 2)

	ep, err := w.s.Create(ctx, Input{Name: "  Billing  ", URL: " https://hooks.example.com/mikan ", Enabled: true,
		Events: []string{events.NodeUp, events.PaymentPaid, events.NodeUp}})
	if err != nil {
		t.Fatal(err)
	}
	if ep.Name != "Billing" || ep.Url != "https://hooks.example.com/mikan" || !ep.Enabled || ep.AllowLocal {
		t.Errorf("saved %+v", ep)
	}
	if want := []string{events.PaymentPaid, events.NodeUp}; !slices.Equal(ep.Events, want) {
		t.Errorf("events %v, want %v", ep.Events, want)
	}
	if ep.LastID != end {
		t.Errorf("the cursor is %d, want the end of the feed (%d)", ep.LastID, end)
	}
	if !strings.HasPrefix(ep.Secret, "whsec_") || len(ep.Secret) != 46 {
		t.Errorf("secret %q", ep.Secret)
	}
	if d := time.Since(time.Unix(ep.CreatedAt, 0)); d < 0 || d > time.Minute || ep.SecretAt != ep.CreatedAt {
		t.Errorf("created %d, secret made %d", ep.CreatedAt, ep.SecretAt)
	}
	// A second one has a secret of its own.
	other, err := w.s.Create(ctx, Input{Name: "x", URL: "https://hooks.example.com/other"})
	if err != nil {
		t.Fatal(err)
	}
	if other.Secret == ep.Secret || other.Enabled || len(other.Events) != 0 {
		t.Errorf("second endpoint %+v", other)
	}
}

// Every field that is wrong is told at once, with what is wrong about it; nothing is saved.
func TestCreateRefusals(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	good := Input{Name: "n", URL: "https://hooks.example.com/x", Enabled: true}
	for name, c := range map[string]struct {
		in   func(*Input)
		want []Problem
	}{
		"a blank name":   {func(in *Input) { in.Name = " \t" }, []Problem{{"name", ErrNameBlank, nil}}},
		"an empty name":  {func(in *Input) { in.Name = "" }, []Problem{{"name", ErrNameBlank, nil}}},
		"no address":     {func(in *Input) { in.URL = "" }, []Problem{{"url", ErrURLScheme, nil}}},
		"another scheme": {func(in *Input) { in.URL = "ftp://hooks.example.com/x" }, []Problem{{"url", ErrURLScheme, nil}}},
		"credentials":    {func(in *Input) { in.URL = "https://u:p@hooks.example.com/x" }, []Problem{{"url", ErrURLInvalid, nil}}},
		"a bad port":     {func(in *Input) { in.URL = "https://hooks.example.com:0/x" }, []Problem{{"url", ErrURLPort, nil}}},
		"localhost":      {func(in *Input) { in.URL = "http://localhost:8080/x" }, []Problem{{"url", ErrLocalAddress, nil}}},
		"a private ip":   {func(in *Input) { in.URL = "http://192.168.1.5/x" }, []Problem{{"url", ErrLocalAddress, nil}}},
		"the metadata":   {func(in *Input) { in.URL = "http://169.254.169.254/latest/" }, []Problem{{"url", ErrBlockedAddress, nil}}},
		"the metadata of a cloud, allowing local targets": {func(in *Input) {
			in.URL, in.AllowLocal = "http://169.254.169.254/latest/", true
		}, []Problem{{"url", ErrBlockedAddress, nil}}},
		"an event that is none": {func(in *Input) { in.Events = []string{"payment.paid", "payment.payd", "webhook.test"} },
			[]Problem{{"events", ErrEventUnknown, "payment.payd"}, {"events", ErrEventUnknown, "webhook.test"}}},
		"everything at once": {func(in *Input) {
			in.Name, in.URL, in.Events = "", "file:///etc/passwd", []string{"x"}
		}, []Problem{{"name", ErrNameBlank, nil}, {"url", ErrURLScheme, nil}, {"events", ErrEventUnknown, "x"}}},
	} {
		in := good
		c.in(&in)
		_, err := w.s.Create(ctx, in)
		var inv *InvalidError
		if !errors.As(err, &inv) {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if len(inv.Problems) != len(c.want) {
			t.Errorf("%s: %v, want %v", name, inv.Problems, c.want)
			continue
		}
		for i, p := range inv.Problems {
			if p.Field != c.want[i].Field || !errors.Is(p.Err, c.want[i].Err) || p.Value != c.want[i].Value {
				t.Errorf("%s: problem %d is %+v, want %+v", name, i, p, c.want[i])
			}
		}
	}
	if n, err := w.st.Q.CountWebhooks(ctx); err != nil || n != 0 {
		t.Fatalf("%d endpoints after refusals (%v)", n, err)
	}
	// What was refused for the want of the switch goes through with it.
	in := good
	in.URL, in.AllowLocal = "http://192.168.1.5:5678/webhook", true
	if _, err := w.s.Create(ctx, in); err != nil {
		t.Fatalf("a local target that is allowed: %v", err)
	}
}

// The panel makes no more workers than it will hold.
func TestCreateLimit(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	for i := range MaxEndpoints {
		if _, err := w.s.Create(ctx, Input{Name: "n", URL: "https://hooks.example.com/" + string(rune('a'+i))}); err != nil {
			t.Fatalf("endpoint %d: %v", i, err)
		}
	}
	if _, err := w.s.Create(ctx, Input{Name: "n", URL: "https://hooks.example.com/z"}); !errors.Is(err, ErrLimit) {
		t.Fatalf("one more: %v", err)
	}
	// A refusal of the fields is told before the limit is looked at, and a deleted one makes room.
	var inv *InvalidError
	if _, err := w.s.Create(ctx, Input{}); !errors.As(err, &inv) {
		t.Fatalf("invalid input at the limit: %v", err)
	}
	rows, _ := w.st.Q.ListWebhooks(ctx)
	if err := w.s.Delete(ctx, rows[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.Create(ctx, Input{Name: "n", URL: "https://hooks.example.com/z"}); err != nil {
		t.Fatalf("after a delete: %v", err)
	}
}

// A change is only what is set; the endpoint is judged as it will be, so a local address
// cannot be kept by taking the switch away; what is not found or refused changes nothing.
func TestUpdate(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	ep := w.endpointIn(Input{Name: "one", URL: "https://hooks.example.com/a", Events: []string{events.PaymentPaid}, Enabled: true})
	str := func(s string) *string { return &s }
	flag := func(b bool) *bool { return &b }
	list := func(s ...string) *[]string { return &s }

	got, err := w.s.Update(ctx, ep.ID, Patch{Name: str(" two ")})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "two" || got.Url != ep.Url || got.Secret != ep.Secret || got.LastID != ep.LastID || !slices.Equal(got.Events, ep.Events) || !got.Enabled {
		t.Errorf("a rename changed more: %+v", got)
	}
	got, err = w.s.Update(ctx, ep.ID, Patch{URL: str("http://hooks.example.com:8080/b"), Enabled: flag(false), Events: list(events.NodeDown, events.NodeUp, events.NodeDown)})
	if err != nil {
		t.Fatal(err)
	}
	if got.Url != "http://hooks.example.com:8080/b" || got.Enabled || !slices.Equal(got.Events, []string{events.NodeDown, events.NodeUp}) || got.Name != "two" {
		t.Errorf("update %+v", got)
	}
	// An empty choice is every event.
	if got, err = w.s.Update(ctx, ep.ID, Patch{Events: list()}); err != nil || len(got.Events) != 0 {
		t.Errorf("every event: %v %v", got.Events, err)
	}

	// Refused: the row stays as it was.
	before := w.get(ep.ID)
	for name, p := range map[string]Patch{
		"a blank name":    {Name: str("  ")},
		"a bad address":   {URL: str("gopher://x.example/")},
		"a metadata host": {URL: str("http://169.254.169.254/")},
		"an unknown type": {Events: list("payment.paid", "nope")},
		"a local address": {URL: str("http://10.0.0.5/x")},
	} {
		var inv *InvalidError
		if _, err := w.s.Update(ctx, ep.ID, p); !errors.As(err, &inv) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if after := w.get(ep.ID); !equal(before, after) {
		t.Errorf("a refused change changed the row:\n%+v\n%+v", before, after)
	}
	// The switch decides about a local address, in both directions.
	if _, err := w.s.Update(ctx, ep.ID, Patch{URL: str("http://10.0.0.5/x"), AllowLocal: flag(true)}); err != nil {
		t.Fatalf("a local address with the switch: %v", err)
	}
	var inv *InvalidError
	if _, err := w.s.Update(ctx, ep.ID, Patch{AllowLocal: flag(false)}); !errors.As(err, &inv) || len(inv.Problems) != 1 || inv.Problems[0].Field != "url" || !errors.Is(inv.Problems[0].Err, ErrLocalAddress) {
		t.Fatalf("the switch taken away from a local address: %v", err)
	}
	if !w.get(ep.ID).AllowLocal {
		t.Fatal("the switch was taken away although the address is local")
	}
	if _, err := w.s.Update(ctx, ep.ID+1000, Patch{Name: str("x")}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an endpoint that is not there: %v", err)
	}
}

func equal(a, b db.WebhookEndpoint) bool {
	return a.ID == b.ID && a.Name == b.Name && a.Url == b.Url && a.Secret == b.Secret && slices.Equal(a.Events, b.Events) &&
		a.Enabled == b.Enabled && a.AllowLocal == b.AllowLocal && a.LastID == b.LastID && a.CreatedAt == b.CreatedAt && a.SecretAt == b.SecretAt
}

// An endpoint that is now told of more types does not get the week's old events of them: they
// start from now. One that is behind keeps what it was owed, and what lies after it.
func TestWidenStartsTheNewTypesFromNow(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	t.Run("up to date", func(t *testing.T) {
		ep := w.endpointIn(Input{Name: "n", URL: "https://hooks.example.com/x", Events: []string{events.PaymentPaid}})
		w.emit(events.NodeDown, 0)
		end := w.emit(events.NodeUp, 0)
		if got, err := w.s.Update(ctx, ep.ID, Patch{Events: &[]string{events.PaymentPaid, events.NodeDown}}); err != nil || got.LastID != end {
			t.Fatalf("widened to %d, want %d: %v", got.LastID, end, err)
		}
		// The same for "every event".
		if got, err := w.s.Update(ctx, ep.ID, Patch{Events: &[]string{}}); err != nil || got.LastID != end {
			t.Fatalf("widened to every event: %d, %v", got.LastID, err)
		}
	})
	t.Run("behind", func(t *testing.T) {
		ep := w.endpointIn(Input{Name: "n", URL: "https://hooks.example.com/x", Events: []string{events.PaymentPaid}})
		w.emit(events.PaymentPaid, 1) // owed
		w.emit(events.NodeDown, 0)
		w.emit(events.NodeUp, 0)
		if got, err := w.s.Update(ctx, ep.ID, Patch{Events: &[]string{events.PaymentPaid, events.NodeDown}}); err != nil || got.LastID != ep.LastID {
			t.Fatalf("the cursor of an endpoint that was owed an event moved to %d (from %d): %v", got.LastID, ep.LastID, err)
		}
	})
	t.Run("narrowed or the same", func(t *testing.T) {
		renamed := "renamed"
		for name, p := range map[string]Patch{
			"narrowed":  {Events: &[]string{events.PaymentPaid}},
			"reordered": {Events: &[]string{events.NodeDown, events.PaymentPaid, events.NodeDown}},
			"renamed":   {Name: &renamed},
		} {
			ep := w.endpointIn(Input{Name: "n", URL: "https://hooks.example.com/x", Events: []string{events.PaymentPaid, events.NodeDown}})
			w.emit(events.NodeUp, 0)
			if got, err := w.s.Update(ctx, ep.ID, p); err != nil || got.LastID != ep.LastID {
				t.Errorf("%s: the cursor is %d, want %d (%v)", name, got.LastID, ep.LastID, err)
			}
		}
	})
}

// A new secret replaces the old at once, the row says when it was made, and what is not there
// is not found.
func TestRotate(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	ep := w.endpointIn(Input{Name: "n", URL: "https://hooks.example.com/x"})
	got, err := w.s.Rotate(ctx, ep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Secret == ep.Secret || !strings.HasPrefix(got.Secret, "whsec_") || w.get(ep.ID).Secret != got.Secret {
		t.Errorf("rotated to %q from %q, kept %q", got.Secret, ep.Secret, w.get(ep.ID).Secret)
	}
	if got.Url != ep.Url || got.LastID != ep.LastID || got.Name != ep.Name {
		t.Errorf("a rotation changed more: %+v", got)
	}
	if _, err := w.s.Rotate(ctx, ep.ID+1000); !errors.Is(err, ErrNotFound) {
		t.Errorf("an endpoint that is not there: %v", err)
	}
}

// A deleted endpoint takes its log with it, and none of the calls finds it any more.
func TestDelete(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	r := newReceiver(t)
	ep := w.endpoint(r)
	if _, err := w.s.Test(ctx, ep.ID); err != nil {
		t.Fatal(err)
	}
	if len(w.log(ep.ID)) != 1 {
		t.Fatal("the test is not in the log")
	}
	if err := w.s.Delete(ctx, ep.ID); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := w.st.DB.QueryRow("SELECT count(*) FROM webhook_deliveries").Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("%d rows of the log are left (%v)", rows, err)
	}
	if err := w.s.Delete(ctx, ep.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a second delete: %v", err)
	}
	if _, err := w.s.Test(ctx, ep.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a test: %v", err)
	}
	if _, err := w.s.Deliveries(ctx, ep.ID, 0, "", 10); !errors.Is(err, ErrNotFound) {
		t.Errorf("the log: %v", err)
	}
}

// The list says, for each endpoint, how many events wait for it (of the types it is told of,
// after its cursor) and how its newest delivery went.
func TestList(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	r := newReceiver(t)
	all := w.endpoint(r)
	paid := w.endpoint(r, events.PaymentPaid)
	quiet := w.endpointIn(Input{Name: "quiet", URL: "https://hooks.example.com/q", Events: []string{events.TrialStarted}})
	w.emit(events.PaymentPaid, 1)
	w.emit(events.NodeDown, 0)
	w.emit(events.PaymentPaid, 2)

	list, err := w.s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 || list[0].ID != all.ID || list[1].ID != paid.ID || list[2].ID != quiet.ID {
		t.Fatalf("the list %+v", list)
	}
	for i, want := range []int64{3, 2, 0} {
		if list[i].Waiting != want || list[i].Last != nil {
			t.Errorf("endpoint %d: %d waiting (want %d), last %+v", i, list[i].Waiting, want, list[i].Last)
		}
	}
	// A delivery moves the cursor and shows as the last one.
	ids := w.feedIDs()
	r.answer(500)
	ev := events.Event{ID: ids[0], Type: events.PaymentPaid}
	if res, err := w.s.send(ctx, w.get(all.ID), ev); err != nil || res.OK {
		t.Fatalf("the failing delivery: %+v %v", res, err)
	}
	if res, err := w.s.send(ctx, w.get(all.ID), ev); err != nil || !res.OK {
		t.Fatalf("the delivery: %+v %v", res, err)
	}
	list, _ = w.s.List(ctx)
	if list[0].Waiting != 2 || list[0].Last == nil || list[0].Last.Status != "delivered" || list[0].Last.Attempts != 2 || list[0].Last.EventID.Int64 != ids[0] {
		t.Errorf("after a delivery: %d waiting, last %+v", list[0].Waiting, list[0].Last)
	}
	if list[1].Waiting != 2 || list[1].Last != nil {
		t.Errorf("another endpoint moved: %+v", list[1])
	}
}

func (w *world) feedIDs() (ids []int64) {
	w.t.Helper()
	rows, err := w.st.Q.ListAddonEvents(context.Background(), db.ListAddonEventsParams{Types: []string{}, Lim: 1000})
	if err != nil {
		w.t.Fatal(err)
	}
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return ids
}

// The log is read a page at a time, newest first, and can be narrowed to one status.
func TestDeliveriesPages(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	ep := w.endpointIn(Input{Name: "n", URL: "https://hooks.example.com/x"})
	other := w.endpointIn(Input{Name: "o", URL: "https://hooks.example.com/y"})
	record := func(id, event int64, status string) {
		t.Helper()
		err := w.st.Q.RecordWebhookAttempt(ctx, db.RecordWebhookAttemptParams{EndpointID: id, EventID: sql.NullInt64{Int64: event, Valid: event != 0},
			Type: events.PaymentPaid, Status: status, ResponseCode: 200, CreatedAt: time.Now().Unix()})
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := int64(1); i <= 7; i++ {
		status := "delivered"
		if i%3 == 0 {
			status = "retrying"
		}
		record(ep.ID, i, status)
	}
	record(other.ID, 1, "delivered")
	record(ep.ID, 0, "failed") // a test

	var seen []int64
	before := int64(0)
	for range 10 {
		page, err := w.s.Deliveries(ctx, ep.ID, before, "", 3)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range page {
			if d.EndpointID != ep.ID {
				t.Fatalf("another endpoint's row %+v", d)
			}
			seen = append(seen, d.ID)
		}
		if len(page) < 3 {
			break
		}
		before = page[len(page)-1].ID
	}
	if !slices.IsSortedFunc(seen, func(a, b int64) int { return int(b - a) }) || len(slices.Compact(slices.Clone(seen))) != 8 {
		t.Fatalf("pages gave %v, want the 8 rows newest first, once each", seen)
	}
	for status, want := range map[string]int{"delivered": 5, "retrying": 2, "failed": 1, "": 8} {
		rows, err := w.s.Deliveries(ctx, ep.ID, 0, status, 100)
		if err != nil || len(rows) != want {
			t.Errorf("status %q: %d rows (%v), want %d", status, len(rows), err, want)
		}
		for _, d := range rows {
			if status != "" && d.Status != status {
				t.Errorf("a %s row among %q", d.Status, status)
			}
		}
	}
}

// What is kept of an event is one row, whatever it takes to deliver it: the first attempt makes
// it and the rest update it, a repeat after the event was taken is one more attempt, and a test
// has a row of its own each time.
func TestLogHasARowPerEvent(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	r := newReceiver(t)
	ep := w.endpoint(r)
	older := w.emit(events.PaymentPaid, 1)
	id := w.emit(events.PaymentPaid, 2)
	ev := events.Event{ID: id, Type: events.PaymentPaid}

	r.answer(500, 404, 302)
	for i, want := range []struct {
		status, failure string
		code            int
		ok              bool
	}{{"retrying", FailStatus, 500, false}, {"retrying", FailStatus, 404, false}, {"retrying", FailRedirect, 302, false}, {"delivered", "", 204, true}} {
		res, err := w.s.send(ctx, w.get(ep.ID), ev)
		if err != nil || res.OK != want.ok || res.Status != want.code || res.Failure != want.failure {
			t.Fatalf("attempt %d: %+v %v", i, res, err)
		}
		row, ok := w.row(ep.ID, id)
		if !ok || row.Status != want.status || row.Attempts != int64(i+1) || row.ResponseCode != int64(want.code) || row.Error != want.failure || row.Type != events.PaymentPaid {
			t.Fatalf("attempt %d: row %+v", i, row)
		}
	}
	if w.get(ep.ID).LastID != id {
		t.Fatalf("the cursor %d, want %d", w.get(ep.ID).LastID, id)
	}
	// The event is sent once more (the panel stopped between the answer and the cursor):
	// one row, one attempt more, and the cursor does not go back.
	if _, err := w.s.send(ctx, w.get(ep.ID), events.Event{ID: older, Type: events.PaymentPaid}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.send(ctx, w.get(ep.ID), ev); err != nil {
		t.Fatal(err)
	}
	if row, _ := w.row(ep.ID, id); row.Attempts != 5 || row.Status != "delivered" {
		t.Errorf("a repeat: %+v", row)
	}
	if w.get(ep.ID).LastID != id {
		t.Errorf("the cursor went back to %d", w.get(ep.ID).LastID)
	}
	// Two tests are two rows.
	for range 2 {
		if _, err := w.s.Test(ctx, ep.ID); err != nil {
			t.Fatal(err)
		}
	}
	tests := 0
	for _, d := range w.log(ep.ID) {
		if d.Type == TestType {
			tests++
			if d.EventID.Valid || d.Status != "delivered" || d.Attempts != 1 {
				t.Errorf("test row %+v", d)
			}
		}
	}
	if tests != 2 {
		t.Errorf("%d rows of tests", tests)
	}
}

// The log is dropped a week after its last attempt; a delivery whose event left the feed while
// it was being tried is ended, and one whose event is still there is not.
func TestPruneTheLog(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	ep := w.endpointIn(Input{Name: "n", URL: "https://hooks.example.com/x"})
	live := w.emit(events.PaymentPaid, 1)
	now := time.Now()
	add := func(event int64, status string, at time.Time) {
		t.Helper()
		err := w.st.Q.RecordWebhookAttempt(ctx, db.RecordWebhookAttemptParams{EndpointID: ep.ID, EventID: sql.NullInt64{Int64: event, Valid: true},
			Type: events.PaymentPaid, Status: status, CreatedAt: at.Unix()})
		if err != nil {
			t.Fatal(err)
		}
	}
	add(live, "retrying", now)                                   // its event is there
	add(live+100, "retrying", now)                               // its event is gone
	add(live+101, "delivered", now.Add(-LogRetention+time.Hour)) // nearly a week old
	add(live+102, "delivered", now.Add(-LogRetention-time.Hour)) // more than a week old
	add(live+103, "retrying", now.Add(-LogRetention-time.Hour))  // old and never ended

	w.s.prune(ctx)

	got := map[int64]string{}
	for _, d := range w.log(ep.ID) {
		got[d.EventID.Int64] = d.Status
	}
	want := map[int64]string{live: "retrying", live + 100: "failed", live + 101: "delivered"}
	if len(got) != len(want) {
		t.Fatalf("rows %v, want %v", got, want)
	}
	for event, status := range want {
		if got[event] != status {
			t.Errorf("event %d is %q, want %q (rows %v)", event, got[event], status, got)
		}
	}
}

// The log's pruning and its page of an endpoint go by an index, not a scan of the table.
func TestLogQueriesUseTheirIndexes(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	conn, err := w.st.DB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "SET enable_seqscan=off"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ sql, index string }{
		{"DELETE FROM webhook_deliveries WHERE updated_at < 1", "webhook_deliveries_updated"},
		{"SELECT * FROM webhook_deliveries WHERE endpoint_id = 1 AND id < 100 ORDER BY id DESC LIMIT 50", "webhook_deliveries_endpoint"},
		{"UPDATE webhook_deliveries SET status = 'failed' WHERE status = 'retrying' AND event_id IS NOT NULL", "webhook_deliveries_retrying"},
	} {
		rows, err := conn.QueryContext(ctx, "EXPLAIN "+c.sql)
		if err != nil {
			t.Fatalf("%s: %v", c.sql, err)
		}
		var plan []string
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, line)
		}
		rows.Close()
		if text := strings.Join(plan, "; "); !strings.Contains(text, c.index) {
			t.Errorf("%s\n  plan: %s\n  want a search by %s", c.sql, text, c.index)
		}
	}
}
