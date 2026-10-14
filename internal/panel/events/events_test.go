package events_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/events"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/store/storetest"
)

// An event written on a transaction that rolls back is never told, and one written twice
// under the same key is told once.
func TestEmitIsPartOfTheTransaction(t *testing.T) {
	ctx := context.Background()
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Unix(1_800_000_000, 0)
	count := func() int {
		evs, err := st.Q.ListAddonEvents(ctx, db.ListAddonEventsParams{Types: []string{}, Lim: 100})
		if err != nil {
			t.Fatal(err)
		}
		return len(evs)
	}
	boom := errors.New("boom")
	err = st.Tx(ctx, func(q *db.Queries) error {
		if err := events.Emit(ctx, q, now, events.UserCreated, 1, 0, events.User{Source: "admin"}, "user.created:1"); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) || count() != 0 {
		t.Fatalf("a rolled back transaction told %d events (%v)", count(), err)
	}
	// The key of the rolled back one is free: the retry tells it.
	for range 2 {
		if err := st.Tx(ctx, func(q *db.Queries) error {
			return events.Emit(ctx, q, now, events.UserCreated, 1, 0, events.User{Source: "admin"}, "user.created:1")
		}); err != nil {
			t.Fatal(err)
		}
	}
	if count() != 1 {
		t.Fatalf("%d events after a retry", count())
	}
}

// Every type the panel emits is documented once, with data that is valid JSON of the struct
// it names, and no secret: the list PROTOCOL-v2.md is written from.
func TestEveryTypeIsDocumented(t *testing.T) {
	valid := regexp.MustCompile(`^[a-z0-9_]+(\.[a-z0-9_]+)+$`)
	seen := map[string]bool{}
	for _, d := range events.Docs {
		if !valid.MatchString(d.Type) || seen[d.Type] || d.About == "" || strings.Contains(d.About, "—") {
			t.Errorf("%q: not a name, listed twice or no description", d.Type)
		}
		seen[d.Type] = true
		raw, err := json.Marshal(d.Data)
		if err != nil || !json.Valid(raw) || raw[0] != '{' {
			t.Errorf("%s: data %s %v", d.Type, raw, err)
		}
		// What is written is what is read back: no field is lost on the way.
		back := reflect.New(reflect.TypeOf(d.Data))
		if err := json.Unmarshal(raw, back.Interface()); err != nil || !reflect.DeepEqual(back.Elem().Interface(), d.Data) {
			t.Errorf("%s: %s does not read back: %v", d.Type, raw, err)
		}
		for _, secret := range []string{"token", "key", "hwid", "secret", "password", "address"} {
			if strings.Contains(strings.ToLower(string(raw)), `"`+secret) {
				t.Errorf("%s: data names %q", d.Type, secret)
			}
		}
	}
	// Every constant of the feed is in the list but the deprecated bot.* ones.
	for _, typ := range []string{events.PaymentPaid, events.PaymentRefunded, events.UserCreated, events.UserExpiring, events.UserExpired,
		events.UserGranted, events.Traffic90, events.Traffic100, events.TrialStarted, events.TrialNotGiven, events.DeviceBound, events.DeviceUnbound,
		events.NodeDown, events.NodeUp} {
		if !seen[typ] {
			t.Errorf("%s is not documented", typ)
		}
	}
	for _, typ := range []string{events.BotPaid, events.BotRefunded, events.BotTrialNotGiven} {
		if seen[typ] {
			t.Errorf("%s is deprecated and for the bot addon alone", typ)
		}
		if !strings.HasPrefix(typ, "bot.") {
			t.Errorf("%s would not be held back from other addons", typ)
		}
	}
	if len(events.Docs) != 14 {
		t.Errorf("%d types documented", len(events.Docs))
	}
}
