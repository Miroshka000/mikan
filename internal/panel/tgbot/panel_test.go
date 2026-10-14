package tgbot

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mikan/internal/panel/events"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/tgate"
)

// panelEmitting is the panel's side of the bot with its feed replaced.
func (e *env) panelEmitting(emit Emit) *Panel {
	return NewPanel(PanelDeps{Backend: e.panel.d.Backend, Outbox: e.gate.Outbox, Log: e.panel.d.Log, Now: e.clock, Emit: emit})
}

// Each due notice becomes an event for the bot addon, once, and is recorded as told; the
// panel sends nothing itself.
func TestNoticesBecomeEventsOnce(t *testing.T) {
	e := setup(t)
	const anna = 555
	e.link(anna)
	e.expireIn(2 * 24 * time.Hour)
	type emitted struct {
		typ          string
		user, tg     int64
		data, dedupe string
	}
	var got []emitted
	p := e.panelEmitting(func(_ context.Context, typ string, user, tg int64, data any, dedupe string) error {
		raw, _ := json.Marshal(data)
		got = append(got, emitted{typ, user, tg, string(raw), dedupe})
		return nil
	})
	n := e.tg.count()
	p.Notify(e.ctx)
	p.Notify(e.ctx)
	time.Sleep(100 * time.Millisecond)
	if e.sendsTo(anna, n) != 0 {
		t.Fatal("the panel sent a notice itself")
	}
	if len(got) != 1 || got[0].typ != events.UserExpiring || got[0].user != e.user.ID || got[0].tg != anna || !strings.Contains(got[0].data, `"days":3`) {
		t.Fatalf("events: %+v", got)
	}
	if e.noticeRows() != 1 {
		t.Fatal("the notice told as an event is not recorded")
	}
}

// A notice the feed could not take is not recorded, and comes at the next round.
func TestNoticeIsNotLostWhenTheFeedFails(t *testing.T) {
	e := setup(t)
	e.link(555)
	e.expireIn(2 * 24 * time.Hour)
	var failing atomic.Bool
	failing.Store(true)
	var told atomic.Int32
	p := e.panelEmitting(func(context.Context, string, int64, int64, any, string) error {
		if failing.Load() {
			return context.DeadlineExceeded
		}
		told.Add(1)
		return nil
	})
	p.Notify(e.ctx)
	if told.Load() != 0 || e.noticeRows() != 0 {
		t.Fatal("a notice the feed refused is recorded as told")
	}
	failing.Store(false)
	p.Notify(e.ctx)
	p.Notify(e.ctx)
	if told.Load() != 1 || e.noticeRows() != 1 {
		t.Fatalf("told %d times, %d rows", told.Load(), e.noticeRows())
	}
}

// The notices a switch of the admin turned off are not told.
func TestNoticesFollowTheAdminsSwitches(t *testing.T) {
	e := setup(t)
	e.link(555)
	e.expireIn(2 * 24 * time.Hour)
	cfg := Default("ru")
	cfg.Notify.Expire3d = false
	if err := settings.Set(e.ctx, e.set, tgate.KeyConfig, cfg); err != nil {
		t.Fatal(err)
	}
	var told atomic.Int32
	e.panelEmitting(func(context.Context, string, int64, int64, any, string) error { told.Add(1); return nil }).Notify(e.ctx)
	if told.Load() != 0 {
		t.Fatal("a notice that is switched off was told")
	}
}

// Notices told long ago are forgotten in the small hours, once a day, and never sooner.
func TestToldNoticesArePruned(t *testing.T) {
	e := setup(t)
	old := e.clock().Add(-100 * 24 * time.Hour).Unix()
	fresh := e.clock().Add(-10 * 24 * time.Hour).Unix()
	for kind, at := range map[string]int64{"expired": old, "expire_1d": fresh} {
		if _, err := e.st.Q.AddTgNotice(e.ctx, db.AddTgNoticeParams{UserID: e.user.ID, Kind: kind, Period: 1, SentAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	e.mu.Lock()
	e.now = time.Date(2027, 1, 15, 12, 0, 0, 0, time.Local)
	e.mu.Unlock()
	e.panel.Notify(e.ctx)
	if e.noticeRows() != 2 {
		t.Fatal("pruned in the middle of the day")
	}
	e.mu.Lock()
	e.now = time.Date(2027, 1, 16, 3, 5, 0, 0, time.Local)
	e.mu.Unlock()
	e.panel.Notify(e.ctx)
	if e.noticeRows() != 1 {
		t.Fatalf("the old notice stays: %d rows", e.noticeRows())
	}
}
