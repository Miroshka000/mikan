package infraalerts

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/hysteresis"
	"mikan/internal/tgapi"
)

// fakeSender records what the queue sends and answers from fields, so the delivery runs
// without Telegram.
type fakeSender struct {
	calls   []string
	sendErr error
	editErr error
	message int64
}

func (f *fakeSender) Send(_ context.Context, chat int64, text string, _ *tgapi.Keyboard, _ bool) (tgapi.Message, error) {
	f.calls = append(f.calls, "send "+text)
	return tgapi.Message{MessageID: f.message}, f.sendErr
}

func (f *fakeSender) SendTo(_ context.Context, chat string, text string, silent bool) (tgapi.Message, error) {
	f.calls = append(f.calls, "sendto "+chat+" "+text)
	return tgapi.Message{MessageID: f.message}, f.sendErr
}

func (f *fakeSender) SendToTopic(_ context.Context, chat string, thread int64, text string) (tgapi.Message, error) {
	f.calls = append(f.calls, "topic "+chat+"/"+strconv.FormatInt(thread, 10)+" "+text)
	return tgapi.Message{MessageID: f.message}, f.sendErr
}

func (f *fakeSender) EditTo(_ context.Context, chat string, msg int64, text string) error {
	f.calls = append(f.calls, "edit "+text)
	return f.editErr
}

func (f *fakeSender) PinTo(_ context.Context, chat string, msg int64) error {
	f.calls = append(f.calls, "pin")
	return nil
}

func quietMonitor(now time.Time) *Monitor {
	return &Monitor{log: slog.New(slog.NewTextHandler(io.Discard, nil)), now: func() time.Time { return now }}
}

func TestSendPendingRoutesEachTarget(t *testing.T) {
	m := quietMonitor(time.Unix(1000, 0))
	f := &fakeSender{message: 7}
	cfg := Default()
	cfg.AdminEnabled, cfg.PublicEnabled, cfg.PublicChannel = true, true, "@channel"
	st := persistentState{Pending: []delivery{
		{Key: "a", Target: targetAdmin, Text: "to admin"},
		{Key: "b", Target: targetSummary, Text: "summary"},
		{Key: "c", Target: targetPublicChange, Text: "change"},
	}}
	m.sendPending(context.Background(), &st, cfg, outlets{tg: f, admin: 55, adminSet: true})
	want := []string{"send to admin", "sendto @channel summary", "pin", "edit summary", "sendto @channel change"}
	if len(f.calls) != len(want) {
		t.Fatalf("calls = %q, want %q", f.calls, want)
	}
	for i := range want {
		if f.calls[i] != want[i] {
			t.Fatalf("call %d = %q, want %q", i, f.calls[i], want[i])
		}
	}
	if len(st.Pending) != 0 || st.PublicMessage != 7 || !st.PublicPinned {
		t.Fatalf("after delivery: %+v", st)
	}
}

func TestSendPendingSkipsSwitchedOffTargets(t *testing.T) {
	m := quietMonitor(time.Unix(1000, 0))
	f := &fakeSender{}
	cfg := Default() // the admin is off and so is the public channel
	st := persistentState{Pending: []delivery{
		{Key: "a", Target: targetAdmin, Text: "x"},
		{Key: "b", Target: targetSummary, Text: "y"},
		{Key: "c", Target: targetPublicChange, Text: "z"},
	}}
	m.sendPending(context.Background(), &st, cfg, outlets{tg: f, admin: 0, adminSet: false})
	if len(f.calls) != 0 || len(st.Pending) != 0 {
		t.Fatalf("calls %q, pending %+v", f.calls, st.Pending)
	}
}

func TestSendPendingRetriesAndDropsStale(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	m := quietMonitor(now)
	f := &fakeSender{sendErr: context.DeadlineExceeded}
	cfg := Default()
	cfg.AdminEnabled = true
	st := persistentState{Pending: []delivery{
		{Key: "fresh", Target: targetAdmin, Text: "x", CreatedAt: now.Unix()},
		{Key: "stale", Target: targetAdmin, Text: "y", CreatedAt: now.Unix() - int64(deliveryMaxAge/time.Second) - 1},
		{Key: "later", Target: targetAdmin, Text: "z", CreatedAt: now.Unix(), NextAttemptAt: now.Unix() + 60},
	}}
	m.sendPending(context.Background(), &st, cfg, outlets{tg: f, admin: 1, adminSet: true})
	if len(f.calls) != 1 {
		t.Fatalf("calls = %q", f.calls)
	}
	if len(st.Pending) != 2 || st.Pending[0].Key != "fresh" || st.Pending[0].Attempts != 1 || st.Pending[0].NextAttemptAt != now.Unix()+5 || st.Pending[1].Key != "later" {
		t.Fatalf("pending = %+v", st.Pending)
	}
}

// The wait before a message is tried again doubles from five seconds; a 429 waits as long as
// Telegram asked when that is longer, and no wait passes the hour.
func TestRetryDeliveryPauses(t *testing.T) {
	tooMany := func(after time.Duration) error {
		return &tgapi.APIError{Code: 429, Description: "Too Many Requests", RetryAfter: after}
	}
	for _, c := range []struct {
		name     string
		attempts int // made before this failure
		err      error
		want     time.Duration
	}{
		{"first failure", 0, context.DeadlineExceeded, 5 * time.Second},
		{"second", 1, context.DeadlineExceeded, 10 * time.Second},
		{"third", 2, context.DeadlineExceeded, 20 * time.Second},
		{"last before it is given up", maxDeliveryAttempts - 2, context.DeadlineExceeded, 320 * time.Second},
		{"429 that asks for more", 0, tooMany(30 * time.Second), 30 * time.Second},
		{"429 that asks for less", 3, tooMany(time.Second), 40 * time.Second},
		{"429 that asks for more than the hour", 0, tooMany(3 * time.Hour), time.Hour},
	} {
		d := delivery{Attempts: c.attempts}
		if !retryDelivery(&d, 1000, c.err) || d.Attempts != c.attempts+1 || d.NextAttemptAt != 1000+int64(c.want/time.Second) {
			t.Errorf("%s: got %+v, want the next attempt %s after now", c.name, d, c.want)
		}
	}
}

func TestSendPendingPermanentErrorIsDropped(t *testing.T) {
	m := quietMonitor(time.Unix(1000, 0))
	f := &fakeSender{sendErr: &tgapi.APIError{Code: 403, Description: "Forbidden: bot was blocked"}}
	cfg := Default()
	cfg.AdminEnabled = true
	st := persistentState{Pending: []delivery{{Key: "a", Target: targetAdmin, Text: "x"}}}
	m.sendPending(context.Background(), &st, cfg, outlets{tg: f, admin: 1, adminSet: true})
	if len(st.Pending) != 0 {
		t.Fatalf("a refused message stayed: %+v", st.Pending)
	}
}

func TestSendSummaryForgetsAMessageTheChannelLost(t *testing.T) {
	m := quietMonitor(time.Unix(1000, 0))
	f := &fakeSender{editErr: &tgapi.APIError{Code: 400, Description: "Bad Request: message to edit not found"}}
	cfg := Default()
	cfg.PublicEnabled, cfg.PublicChannel = true, "@channel"
	st := persistentState{PublicMessage: 9, PublicPinned: true}
	if err := m.sendSummary(context.Background(), &st, cfg, f, "text"); err == nil {
		t.Fatal("the edit error was lost")
	}
	if st.PublicMessage != 0 || st.PublicPinned {
		t.Fatalf("the lost message was kept: %+v", st)
	}
}

func TestDropPendingKeepsTheOthersInOrder(t *testing.T) {
	st := persistentState{Pending: []delivery{
		{Key: "1", Target: targetAdmin}, {Key: "2", Target: targetSummary}, {Key: "3", Target: targetPublicChange}, {Key: "4", Target: targetAdmin},
	}}
	dropPending(&st, targetSummary, targetPublicChange)
	if len(st.Pending) != 2 || st.Pending[0].Key != "1" || st.Pending[1].Key != "4" {
		t.Fatalf("pending = %+v", st.Pending)
	}
}

// The texts are the admin's and the channel's words: these are checked byte for byte, in
// both languages.
func TestTextsBothLanguages(t *testing.T) {
	cfg := Default()
	cases := []struct{ got, want string }{
		{PublicText("a", hysteresis.Healthy), "🟢 a — Работает"},
		{PublicText("a", hysteresis.Unknown), "⚪ a — Проверяем"},
		{PublicTextEN("a", hysteresis.Degraded), "🟡 a — Issues"},
		{PublicTextEN("a", hysteresis.Unavailable), "🔴 a — Unavailable"},
		{eventFor(cfg, eventNode, "n<", hysteresis.Healthy, &ruTexts), "🟢 <b>n&lt;</b> — Восстановлен"},
		{eventFor(cfg, eventNode, "n", hysteresis.Degraded, &enTexts), "🟡 <b>n</b> — Degraded"},
		{eventFor(cfg, eventInbound, "n", hysteresis.Unavailable, &enTexts), "🔴 <b>n</b> — Unavailable"},
		{warpReason(nodeapi.WarpStatus{Error: "timeout"}, "1.2.3.4:2408", "ru"), "Причина: нет ответа от endpoint 1.2.3.4:2408 по UDP: возможно, хостер блокирует UDP, попробуйте другой endpoint"},
		{warpReason(nodeapi.WarpStatus{Error: "timeout"}, "1.2.3.4:2408", "en"), "Reason: no answer from the WARP endpoint 1.2.3.4:2408 over UDP: the host may block UDP, try another endpoint"},
		{warpReason(nodeapi.WarpStatus{}, "", "en"), "Reason: check failed"},
		{warpReason(nodeapi.WarpStatus{Error: "x", Detail: "detail"}, "", "ru"), "Причина: detail"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}
