package tgbot

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mikan/internal/panel/store/db"
	"mikan/internal/panel/tgate"
)

func (e *env) link(tg int64) {
	e.t.Helper()
	if err := e.st.Q.LinkTg(e.ctx, db.LinkTgParams{UserID: e.user.ID, TgID: tg, CreatedAt: 1}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) expireIn(d time.Duration) {
	e.t.Helper()
	exp := e.clock().Add(d).Unix()
	if _, err := e.st.DB.ExecContext(e.ctx, "UPDATE users SET expires_at = $1 WHERE id = $2", exp, e.user.ID); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) noticeRows() int {
	e.t.Helper()
	var n int
	if err := e.st.DB.QueryRowContext(e.ctx, "SELECT count(*) FROM tg_notices").Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *env) sendsTo(chat int64, from int) int {
	e.tg.mu.Lock()
	defer e.tg.mu.Unlock()
	n := 0
	for _, c := range e.tg.calls[min(from, len(e.tg.calls)):] {
		if c.method == "sendMessage" && c.body["chat_id"] == float64(chat) {
			n++
		}
	}
	return n
}

func (e *env) advance(d time.Duration) {
	e.mu.Lock()
	e.now = e.now.Add(d)
	e.mu.Unlock()
}

func until(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The pre-checkout answer has Telegram's ten seconds. Whatever else is slow, it goes out
// at once: the update loop does not wait for the other calls to Telegram.
func TestPreCheckoutDoesNotWaitForOtherCalls(t *testing.T) {
	e := setup(t)
	e.tg.mu.Lock()
	e.tg.stall = map[string]time.Duration{"answerCallbackQuery": 2500 * time.Millisecond}
	e.tg.mu.Unlock()
	n := e.tg.count()
	start := time.Now()
	e.press(555, 1, "m")
	e.tg.push(tgate.Update{PreCheckoutQuery: &tgate.PreCheckoutQuery{ID: "q1", From: tgate.User{ID: 555}, Currency: "XTR", TotalAmount: 10, InvoicePayload: "p"}})
	e.tg.until(t, n, func(cs []call) bool { _, ok := find(cs, "answerPreCheckoutQuery"); return ok })
	if took := time.Since(start); took > 1500*time.Millisecond {
		t.Fatalf("the pre-checkout waited %s for a callback answer", took)
	}
}

// render is one pass: a value that itself holds {something} is not filled in again, and
// the result does not depend on the order the map is walked in.
func TestRenderIsOnePassAndDeterministic(t *testing.T) {
	vars := map[string]string{"name": "{used} <b>", "used": "5 GB", "brand": "Mikan", "left": "1 GB"}
	want := "Mikan: {used} &lt;b&gt;, 5 GB, {unknown}, {open"
	for range 200 {
		if got := render("{brand}: {name}, {used}, {unknown}, {open", vars); got != want {
			t.Fatalf("render: %q, want %q", got, want)
		}
	}
	if got := render("a < b {brand}", vars); got != "a &lt; b Mikan" {
		t.Fatalf("the text is escaped: %q", got)
	}
}

// A broadcast goes into the outbox in batches, and all of it is sent.
func TestBroadcastIsFedInBatches(t *testing.T) {
	s, c := newSink(t)
	o := tgate.NewOutbox(c, fast, time.Now, nil, nil)
	run(t, o)
	b := NewPanel(PanelDeps{})
	chats := make([]int64, 1700)
	for i := range chats {
		chats[i] = int64(1000 + i)
	}
	var sentOK atomic.Int32
	var mu sync.Mutex
	most := 0
	queue := func(chat int64) {
		o.Bulk(chat, say(chat, "x"), func(err error) {
			if err == nil {
				sentOK.Add(1)
			}
		})
		mu.Lock()
		most = max(most, o.BulkPending())
		mu.Unlock()
	}
	first := bulkBatch
	for _, ch := range chats[:first] {
		queue(ch)
	}
	b.feed(o, chats[first:], queue)
	s.wait(t, len(chats))
	until(t, "all are sent", func() bool { return int(sentOK.Load()) == len(chats) })
	mu.Lock()
	defer mu.Unlock()
	if most > bulkBatch+bulkLow {
		t.Fatalf("the lane held %d messages at once", most)
	}
}

func TestBroadcastFeedStopsWithTheOutbox(t *testing.T) {
	_, c := newSink(t)
	o := tgate.NewOutbox(c, fast, time.Now, nil, nil)
	b := NewPanel(PanelDeps{})
	b.bcast = BroadcastProgress{Total: 3}
	ctx, cancel := context.WithCancel(context.Background())
	go o.Run(ctx)
	cancel()
	<-o.Stopped()
	b.feed(o, []int64{1, 2, 3}, func(int64) {})
	if p := b.Progress(); p.Failed != 3 || p.Active() {
		t.Fatalf("what a stopped outbox did not send is failed, and the progress ends: %+v", p)
	}
}
