package tgate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// sink is a Bot API that records sendMessage calls; reply decides the answer to each.
type sink struct {
	mu    sync.Mutex
	sent  []sent
	reply func(n int, chat int64) (code int, retryAfter int)
}

type sent struct {
	chat int64
	text string
	at   time.Time
}

func (s *sink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ChatID int64  `json:"chat_id"`
		Text   string `json:"text"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	n := len(s.sent)
	s.sent = append(s.sent, sent{body.ChatID, body.Text, time.Now()})
	reply := s.reply
	s.mu.Unlock()
	if reply != nil {
		if code, after := reply(n, body.ChatID); code != 200 {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": code, "description": "nope", "parameters": map[string]int{"retry_after": after}})
			return
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": n + 1, "chat": map[string]any{"id": body.ChatID}}})
}

func (s *sink) all() []sent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]sent(nil), s.sent...)
}

var fast = Limits{Total: 1000, Bulk: 1000, PerChat: 1000, Burst: 1000, Workers: 1, MaxTries: 3}

func newSink(t *testing.T) (*sink, *Client) {
	t.Helper()
	s := &sink{}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return s, NewClient(srv.URL, "1:x", nil)
}

func say(chat int64, text string) func(context.Context, *Client) error {
	return func(ctx context.Context, c *Client) error {
		_, err := c.Send(ctx, chat, text, nil, false)
		return err
	}
}

func run(t *testing.T, o *Outbox) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go o.Run(ctx)
}

// wait until n messages have gone out.
func (s *sink) wait(t *testing.T, n int) []sent {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := s.all(); len(got) >= n {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("only %d of %d sent", len(s.all()), n)
	return nil
}

// A user waiting for an answer never waits behind a broadcast.
func TestOutboxRepliesFirst(t *testing.T) {
	s, c := newSink(t)
	o := NewOutbox(c, fast, time.Now, nil, nil)
	for chat := int64(1); chat <= 50; chat++ {
		o.Bulk(chat, say(chat, "news"), nil)
	}
	o.Notice(500, say(500, "expiring"), nil)
	o.Reply(999, "", 1, say(999, "menu"))
	run(t, o)
	got := s.wait(t, 52)
	if got[0].chat != 999 || got[1].chat != 500 {
		t.Fatalf("reply, then the notice, then the broadcast: %d %d", got[0].chat, got[1].chat)
	}
}

func TestChannelMessageMethodsUseChannelTarget(t *testing.T) {
	type request struct {
		method string
		body   map[string]any
	}
	var calls []request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		calls = append(calls, request{method, body})
		if method == "sendMessage" {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 17, "chat": map[string]any{"id": -100123, "type": "channel"}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": true})
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "1:x", nil)
	msg, err := c.SendTo(context.Background(), "@status_channel", "working", true)
	if err != nil || msg.MessageID != 17 {
		t.Fatalf("send channel message: %+v %v", msg, err)
	}
	if err := c.EditTo(context.Background(), "@status_channel", msg.MessageID, "recovered"); err != nil {
		t.Fatal(err)
	}
	if err := c.PinTo(context.Background(), "@status_channel", msg.MessageID); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 || calls[0].body["chat_id"] != "@status_channel" || calls[1].body["chat_id"] != "@status_channel" || calls[2].body["chat_id"] != "@status_channel" || calls[2].body["disable_notification"] != true {
		t.Fatalf("channel calls: %+v", calls)
	}
}

// One chat gets about a message a second after a short burst, in order.
func TestOutboxPacesAChat(t *testing.T) {
	s, c := newSink(t)
	lim := fast
	lim.PerChat, lim.Burst, lim.Workers = 20, 2, 3 // 50 ms apart after two
	o := NewOutbox(c, lim, time.Now, nil, nil)
	for i := range 6 {
		o.Reply(7, "", 1, say(7, string(rune('a'+i))))
	}
	o.Reply(8, "", 1, say(8, "other chat"))
	run(t, o)
	got := s.wait(t, 7)
	var mine []sent
	for _, m := range got {
		if m.chat == 7 {
			mine = append(mine, m)
		}
	}
	for i, m := range mine {
		if m.text != string(rune('a'+i)) {
			t.Fatalf("order in a chat: %v", mine)
		}
	}
	if d := mine[5].at.Sub(mine[0].at); d < 150*time.Millisecond {
		t.Fatalf("six messages, burst of two, 20 a second: at least 200 ms, got %s", d)
	}
	if got[len(got)-1].chat == 8 && got[len(got)-1].at.Sub(got[0].at) > 100*time.Millisecond {
		t.Fatal("another chat does not wait for this one's pace")
	}
}

// Fast taps: the menu shows the last screen; the stale ones are not sent at all.
func TestOutboxMergesScreens(t *testing.T) {
	s, c := newSink(t)
	o := NewOutbox(c, fast, time.Now, nil, nil)
	for _, screen := range []string{"sub", "devices", "connect", "renew"} {
		o.Reply(7, "edit:7:100", 1, say(7, screen))
	}
	run(t, o)
	s.wait(t, 1)
	time.Sleep(50 * time.Millisecond)
	if got := s.all(); len(got) != 1 || got[0].text != "renew" {
		t.Fatalf("one edit with the last screen: %+v", got)
	}
}

// A 429 is waited out and the message still goes; nothing is lost.
func TestOutboxWaitsOutFloodControl(t *testing.T) {
	s, c := newSink(t)
	s.reply = func(n int, _ int64) (int, int) {
		if n == 0 {
			return 429, 1
		}
		return 200, 0
	}
	o := NewOutbox(c, fast, time.Now, nil, nil)
	var done atomic.Value
	o.Bulk(1, say(1, "news"), func(err error) { done.Store(err == nil) })
	o.Bulk(2, say(2, "news"), nil)
	start := time.Now()
	run(t, o)
	got := s.wait(t, 3)
	if got[1].at.Sub(start) < 900*time.Millisecond {
		t.Fatalf("the bot waits retry_after before any other message: %s", got[1].at.Sub(start))
	}
	if got[1].chat != 1 {
		t.Fatalf("the refused message keeps its turn: %+v", got)
	}
	deadline := time.Now().Add(time.Second)
	for done.Load() == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if ok, _ := done.Load().(bool); !ok {
		t.Fatal("delivered in the end")
	}
}

// A user who blocked the bot is dropped at once, not retried.
func TestOutboxBlocked(t *testing.T) {
	s, c := newSink(t)
	s.reply = func(int, int64) (int, int) { return 403, 0 }
	var gone atomic.Int64
	o := NewOutbox(c, fast, time.Now, func(chat int64) { gone.Store(chat) }, nil)
	errs := make(chan error, 1)
	o.Notice(42, say(42, "expiring"), func(err error) { errs <- err })
	run(t, o)
	if err := <-errs; err == nil || gone.Load() != 42 {
		t.Fatalf("blocked: %v %d", err, gone.Load())
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(s.all()); n != 1 {
		t.Fatalf("sent %d times", n)
	}
}

func TestBucket(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	b := newBucket(2, 3, now)
	for range 3 {
		if b.wait(now, 1) != 0 {
			t.Fatal("a full bucket gives its burst")
		}
		b.take(1)
	}
	if w := b.wait(now, 1); w != 500*time.Millisecond {
		t.Fatalf("empty at 2 a second: %s", w)
	}
	if b.wait(now.Add(10*time.Second), 3) != 0 || b.tokens != 3 {
		t.Fatalf("refills up to the burst: %v", b.tokens)
	}
}
