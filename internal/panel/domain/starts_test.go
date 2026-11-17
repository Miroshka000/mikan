package domain

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"mikan/internal/panel/events"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

// startsOf are the user.started events of the feed, oldest first.
func startsOf(t *testing.T, st *store.Store) []events.Started {
	t.Helper()
	var out []events.Started
	for _, e := range feedOf(t, st) {
		if e.Type != events.UserStarted {
			continue
		}
		var s events.Started
		if err := json.Unmarshal([]byte(e.Data), &s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func waiting(t *testing.T, st *store.Store, id int64) bool {
	t.Helper()
	w, err := Waiting(context.Background(), st.Q, id)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func traffic(t *testing.T, st *store.Store, id int64, now time.Time) {
	t.Helper()
	if err := st.TxRC(context.Background(), func(q *db.Queries) error {
		return CountTraffic(context.Background(), q, TrafficBatch{Main: map[int64]Bytes{id: {Down: 1000}}}, now)
	}); err != nil {
		t.Fatal(err)
	}
}

// Who waits for the first connection: the tariff decides, the admin's word at creation over
// it; a term without an end has nothing to wait with, and an import's users bring their
// terms running.
func TestWhoWaitsForTheFirstConnection(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, users, _ := setup(t, &now)
	ctx := context.Background()
	tariffs, _ := st.Q.ListTariffs(ctx)
	std := tariffs[1]
	if err := SetTariffLifecycle(ctx, st.Q, std.ID, TariffLifecycle{StartOnConnect: true}); err != nil {
		t.Fatal(err)
	}
	yes, no := true, false
	forever, err := st.Q.CreateTariff(ctx, db.CreateTariffParams{Name: "forever", ResetStrategy: "none", CreatedAt: now.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if err := SetTariffLifecycle(ctx, st.Q, forever.ID, TariffLifecycle{StartOnConnect: true}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		in   CreateInput
		want bool
	}{
		{"the tariff's", CreateInput{TariffID: std.ID}, true},
		{"the admin's no over the tariff's", CreateInput{TariffID: std.ID, StartsOnConnect: &no}, false},
		{"the admin's yes on a tariff without it", CreateInput{TariffID: tariffs[2].ID, StartsOnConnect: &yes}, true},
		{"a tariff without it", CreateInput{TariffID: tariffs[2].ID}, false},
		{"no end to wait with", CreateInput{TariffID: forever.ID}, false},
		{"an import", CreateInput{TariffID: std.ID, Source: UserFromImport}, false},
	} {
		c.in.Name = c.name
		var u db.User
		err := st.Tx(ctx, func(q *db.Queries) (err error) {
			u, err = users.CreateOn(ctx, q, c.in, Patch{})
			return err
		})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := waiting(t, st, u.ID); got != c.want {
			t.Errorf("%s: waits %v, want %v", c.name, got, c.want)
		}
		// Until then the subscription shows its whole term.
		if c.want && u.ExpiresAt.Int64 != now.Add(30*24*time.Hour).Unix() {
			t.Errorf("%s: expires %v", c.name, u.ExpiresAt)
		}
	}
}

// A waiting term's clock stands still: the housekeeping moves the expiry, a traffic period
// counted from its start, the packages with an end and the places bought until the term ends
// on with the clock, so nothing of it runs out; the first counted traffic starts it, from
// then on, and the addons hear of it once.
func TestWaitingTermStartsAtTheFirstConnection(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, users, _ := setup(t, &now)
	ctx := context.Background()
	tariffs, _ := st.Q.ListTariffs(ctx)
	yes := true
	u, err := users.Create(ctx, CreateInput{Name: "a", TariffID: tariffs[1].ID, StartsOnConnect: &yes})
	if err != nil {
		t.Fatal(err)
	}
	other, err := users.Create(ctx, CreateInput{Name: "b", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	term := 30 * 24 * time.Hour
	gift, err := GrantTx(ctx, st.Q, u, GrantSpec{Bytes: GiB, Lifetime: LifetimeDays, Days: 7, Source: SourceAdmin}, now)
	if err != nil {
		t.Fatal(err)
	}
	until := now.Add(term).Unix()
	if err := st.Q.SetUserExtraDevices(ctx, db.SetUserExtraDevicesParams{ExtraDevices: 1, Until: sql.NullInt64{Int64: until, Valid: true}, ID: u.ID}); err != nil {
		t.Fatal(err)
	}

	// Five days and a few rounds later nothing of it has run.
	for range 3 {
		now = now.Add(40 * time.Hour)
		if n, err := users.SlideWaiting(ctx); err != nil || n != 1 {
			t.Fatalf("slide: %d %v", n, err)
		}
	}
	waited := 120 * time.Hour
	got, _ := users.Get(ctx, u.ID)
	if got.ExpiresAt.Int64 != now.Add(term).Unix() || got.PeriodStart != u.PeriodStart+int64(waited/time.Second) {
		t.Fatalf("after the wait: expires %v (want %v), period from %d", got.ExpiresAt, now.Add(term).Unix(), got.PeriodStart)
	}
	if got.ExtraDevicesUntil.Int64 != until+int64(waited/time.Second) {
		t.Fatalf("places bought until %v", got.ExtraDevicesUntil)
	}
	gs, _ := st.Q.ListUserGrants(ctx, u.ID)
	if len(gs) != 1 || gs[0].ID != gift.ID || gs[0].ExpiresAt.Int64 != gift.ExpiresAt.Int64+int64(waited/time.Second) {
		t.Fatalf("the package after the wait: %+v", gs)
	}
	if o, _ := users.Get(ctx, other.ID); o.ExpiresAt.Int64 != other.ExpiresAt.Int64 {
		t.Fatal("a term that does not wait moved")
	}

	// Another's traffic starts nothing; the user's own starts the term, from now.
	traffic(t, st, other.ID, now)
	if !waiting(t, st, u.ID) {
		t.Fatal("another user's traffic started the term")
	}
	now = now.Add(5 * time.Minute)
	traffic(t, st, u.ID, now)
	if waiting(t, st, u.ID) {
		t.Fatal("the first connection did not start the term")
	}
	got, _ = users.Get(ctx, u.ID)
	if got.ExpiresAt.Int64 != now.Add(term).Unix() {
		t.Fatalf("started: expires %v, want %v", got.ExpiresAt, now.Add(term).Unix())
	}
	told := startsOf(t, st)
	if len(told) != 1 || told[0].By != events.StartedByConnection || told[0].ExpiresAt != got.ExpiresAt.Int64 {
		t.Fatalf("told %+v", told)
	}

	// From now on the clock runs: a round moves nothing, more traffic starts nothing.
	now = now.Add(24 * time.Hour)
	if n, err := users.SlideWaiting(ctx); err != nil || n != 0 {
		t.Fatalf("slide after the start: %d %v", n, err)
	}
	traffic(t, st, u.ID, now)
	if again, _ := users.Get(ctx, u.ID); again.ExpiresAt != got.ExpiresAt || len(startsOf(t, st)) != 1 {
		t.Fatalf("the started term moved: %v, told %d times", again.ExpiresAt, len(startsOf(t, st)))
	}
}

// What the admin does to a waiting term: an extension adds to what waits, an end given by
// hand ends the wait and stands as given, "start now" runs the clock from now; and a wait is
// only for a subscription that never connected and whose term is ahead.
func TestAdminAndTheWait(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, users, _ := setup(t, &now)
	ctx := context.Background()
	tariffs, _ := st.Q.ListTariffs(ctx)
	yes, no := true, false
	day := 24 * time.Hour
	waiter := func(name string) db.User {
		t.Helper()
		u, err := users.Create(ctx, CreateInput{Name: name, TariffID: tariffs[1].ID, StartsOnConnect: &yes})
		if err != nil {
			t.Fatal(err)
		}
		return u
	}

	a := waiter("extended")
	a, err := users.Extend(ctx, a.ID, 7)
	if err != nil || !waiting(t, st, a.ID) || a.ExpiresAt.Int64 != now.Add(37*day).Unix() {
		t.Fatalf("extended: %v waits %v %v", a.ExpiresAt, waiting(t, st, a.ID), err)
	}

	b := waiter("dated")
	date := now.Add(90 * day)
	now = now.Add(time.Hour)
	if b, err = users.Update(ctx, b.ID, Patch{ExpiresAt: &date}); err != nil || waiting(t, st, b.ID) || b.ExpiresAt.Int64 != date.Unix() {
		t.Fatalf("dated: %v waits %v %v", b.ExpiresAt, waiting(t, st, b.ID), err)
	}
	if told := startsOf(t, st); len(told) != 1 || told[0].By != events.StartedByAdmin || told[0].ExpiresAt != date.Unix() {
		t.Fatalf("an end by hand told %+v", told)
	}

	c := waiter("started")
	now = now.Add(2 * day)
	if c, err = users.Update(ctx, c.ID, Patch{StartsOnConnect: &no}); err != nil || waiting(t, st, c.ID) {
		t.Fatalf("start now: %v %v", waiting(t, st, c.ID), err)
	}
	if c.ExpiresAt.Int64 != now.Add(30*day).Unix() {
		t.Fatalf("started now: expires %v, want the whole term from now", c.ExpiresAt)
	}
	if told := startsOf(t, st); len(told) != 2 || told[1].By != events.StartedByAdmin {
		t.Fatalf("start now told %+v", told)
	}
	// It never connected: it may wait again, and an ask to wait while waiting changes nothing.
	if c, err = users.Update(ctx, c.ID, Patch{StartsOnConnect: &yes}); err != nil || !waiting(t, st, c.ID) {
		t.Fatalf("waits again: %v %v", waiting(t, st, c.ID), err)
	}
	if _, err := users.Update(ctx, c.ID, Patch{StartsOnConnect: &yes}); err != nil {
		t.Fatalf("asked to wait while waiting: %v", err)
	}

	// Who may not wait.
	connected, err := users.Create(ctx, CreateInput{Name: "connected", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	traffic(t, st, connected.ID, now)
	endless, err := users.Create(ctx, CreateInput{Name: "endless", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := users.Update(ctx, endless.ID, Patch{ClearExpiry: true}); err != nil {
		t.Fatal(err)
	}
	over, err := users.Create(ctx, CreateInput{Name: "over", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	past := now.Add(-time.Hour)
	if _, err := users.Update(ctx, over.ID, Patch{ExpiresAt: &past}); err != nil {
		t.Fatal(err)
	}
	for id, code := range map[int64]string{connected.ID: "already_connected", endless.ID: "no_term_end", over.ID: "term_ended"} {
		_, err := users.Update(ctx, id, Patch{StartsOnConnect: &yes})
		var fe *FieldError
		if !errors.As(err, &fe) || fe.Field != "starts_on_connect" || fe.Code != code || waiting(t, st, id) {
			t.Errorf("user %d asked to wait: %v, want %s", id, err, code)
		}
	}
}

// A wait with no term left to wait with ends at the housekeeping's round, untold: a refund
// took the term, and said so itself.
func TestWaitWithoutTermEnds(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, users, _ := setup(t, &now)
	ctx := context.Background()
	tariffs, _ := st.Q.ListTariffs(ctx)
	yes := true
	u, err := users.Create(ctx, CreateInput{Name: "a", TariffID: tariffs[1].ID, StartsOnConnect: &yes})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, "UPDATE users SET expires_at = $1 WHERE id = $2", now.Unix(), u.ID); err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Minute)
	if n, err := users.SlideWaiting(ctx); err != nil || n != 0 || waiting(t, st, u.ID) {
		t.Fatalf("slide: %d %v, waits %v", n, err, waiting(t, st, u.ID))
	}
	if got, _ := users.Get(ctx, u.ID); got.ExpiresAt.Int64 != now.Add(-10*time.Minute).Unix() {
		t.Fatalf("a term with nothing left moved: %v", got.ExpiresAt)
	}
	if told := startsOf(t, st); len(told) != 0 {
		t.Fatalf("told %+v", told)
	}
}

// A waiting term gets no notices: its expiry moves on with the clock, and every round would
// be a notice of its own. Once it runs, it gets them like any other.
func TestWaitingTermGetsNoNotices(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, users, _ := setup(t, &now)
	ctx := context.Background()
	tariffs, _ := st.Q.ListTariffs(ctx)
	yes := true
	u, err := users.Create(ctx, CreateInput{Name: "a", TariffID: tariffs[0].ID, StartsOnConnect: &yes}) // a trial of 3 days
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Q.LinkTg(ctx, db.LinkTgParams{UserID: u.ID, TgID: 555, CreatedAt: now.Unix()}); err != nil {
		t.Fatal(err)
	}
	listed := func() bool {
		t.Helper()
		rows, err := st.Q.ListNoticeSubscriptions(ctx, now.Unix())
		if err != nil {
			t.Fatal(err)
		}
		return len(rows) == 1 && rows[0].User.ID == u.ID && rows[0].TgID == 555
	}
	if listed() {
		t.Fatal("a waiting term is told about")
	}
	traffic(t, st, u.ID, now)
	if !listed() {
		t.Fatal("a running term is not told about")
	}
	if told := feedOf(t, st); len(told) < 1 || told[len(told)-1].TgID.Int64 != 555 {
		t.Fatalf("the start is not told to the linked account: %+v", told)
	}
}
