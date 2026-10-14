package tgate

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"mikan/internal/panel/billing"
	"mikan/internal/panel/settings"
)

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

// A Bot API that does not answer is given up on after a while: the call is not held for
// the minute a long poll may take.
func TestCallsHaveTheirOwnTimeout(t *testing.T) {
	var hits atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	c := NewClient(srv.URL, "1:x", nil)
	if c.callTimeout != callTimeout || callTimeout > 20*time.Second {
		t.Fatalf("a call's time: %s", c.callTimeout)
	}
	c.callTimeout = 150 * time.Millisecond
	start := time.Now()
	_, err := c.Send(context.Background(), 1, "hi", nil, false)
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("a call that hangs: %v", err)
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("gave up after %s", took)
	}
	if hits.Load() != 1 {
		t.Fatal("the call did not reach the server")
	}
	// A cancelled context stays a cancellation, not an unreachable Telegram.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Send(ctx, 1, "hi", nil, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
	// The pre-checkout answer gets less than other calls: Telegram's ten seconds are short.
	if preCheckoutTimeout >= 10*time.Second || preCheckoutDeadline >= 10*time.Second {
		t.Fatalf("the pre-checkout answer has %s, Telegram gives ten seconds", preCheckoutTimeout)
	}
}

// The saved offset keeps a restart from receiving the last batch again, and a payment
// that could not be applied is offered again, in order, until it is.
func TestOffsetIsSavedAndPaymentsAreRetried(t *testing.T) {
	updateRetryPause = time.Millisecond
	t.Cleanup(func() { updateRetryPause = 2 * time.Second })
	var tries atomic.Int32
	e := setup(t, func(e *env, d *Deps) {
		d.stars = func(_ context.Context, tg int64, payload, charge, currency string, amount int64) error {
			if tries.Add(1) <= 2 {
				return errors.New("database is locked")
			}
			return nil
		}
	})
	pay := Update{Message: &Message{MessageID: 9, From: &User{ID: 555}, Chat: Chat{ID: 555, Type: "private"},
		SuccessfulPayment: &SuccessfulPayment{Currency: "XTR", TotalAmount: 10, InvoicePayload: "p", ChargeID: "ch1"}}}
	e.tg.push(pay)
	e.say(555, "привет") // comes after the payment and waits for it
	n := e.tg.count()
	until(t, "the payment is applied on the third try", func() bool { return tries.Load() == 3 })
	e.tg.wait(t, n, "sendMessage") // the chat message is handled after it
	if tries.Load() != 3 {
		t.Fatalf("an applied payment was offered again: %d tries", tries.Load())
	}
	var rec offsetRecord
	until(t, "the offset is saved", func() bool {
		r, ok, _ := settings.Get[offsetRecord](e.ctx, e.set, KeyOffset)
		rec = r
		return ok && rec.Bot == 1 && rec.Offset >= 1002
	})

	// A restart goes on from there, not from the beginning.
	e.tg.mu.Lock()
	before := len(e.tg.offsets)
	e.tg.mu.Unlock()
	e.gate.Reload()
	until(t, "the restarted bot polls", func() bool {
		e.tg.mu.Lock()
		defer e.tg.mu.Unlock()
		return len(e.tg.offsets) > before+1
	})
	e.tg.mu.Lock()
	first := e.tg.offsets[before:]
	e.tg.mu.Unlock()
	for _, o := range first {
		if o < rec.Offset {
			t.Fatalf("a restarted bot asked from %d, the saved offset is %d: %v", o, rec.Offset, first)
		}
	}
}

// An update that never works is dropped after a few tries (a Stars payment nothing saved
// after many more), and does not stop the bot.
func TestPoisonUpdateDoesNotStopTheBot(t *testing.T) {
	updateRetryPause = time.Millisecond
	t.Cleanup(func() { updateRetryPause = 2 * time.Second })
	var tries atomic.Int32
	e := setup(t, func(e *env, d *Deps) {
		d.stars = func(context.Context, int64, string, string, string, int64) error {
			tries.Add(1)
			return errors.New("always")
		}
	})
	e.tg.push(Update{Message: &Message{MessageID: 9, From: &User{ID: 555}, Chat: Chat{ID: 555, Type: "private"},
		SuccessfulPayment: &SuccessfulPayment{Currency: "XTR", TotalAmount: 10, InvoicePayload: "p", ChargeID: "ch1"}}})
	n := e.tg.count()
	e.say(555, "привет")
	e.tg.wait(t, n, "sendMessage")
	if got := tries.Load(); got != maxPaymentTries {
		t.Fatalf("tried %d times, want %d", got, maxPaymentTries)
	}
}

// A payment saved with its charge but not applied is not offered again: the reconcile
// pass finishes it.
func TestRecordedPaymentIsNotRetried(t *testing.T) {
	updateRetryPause = time.Millisecond
	t.Cleanup(func() { updateRetryPause = 2 * time.Second })
	var tries atomic.Int32
	e := setup(t, func(e *env, d *Deps) {
		d.stars = func(context.Context, int64, string, string, string, int64) error {
			tries.Add(1)
			return fmt.Errorf("%w: apply failed", billing.ErrRecorded)
		}
	})
	e.tg.push(Update{Message: &Message{MessageID: 9, From: &User{ID: 555}, Chat: Chat{ID: 555, Type: "private"},
		SuccessfulPayment: &SuccessfulPayment{Currency: "XTR", TotalAmount: 10, InvoicePayload: "p", ChargeID: "ch1"}}})
	n := e.tg.count()
	e.say(555, "привет")
	e.tg.wait(t, n, "sendMessage")
	if got := tries.Load(); got != 1 {
		t.Fatalf("a saved payment was offered %d times", got)
	}
}

// A payment that matches no invoice never will: not offered again.
func TestPaymentWithoutInvoiceIsNotRetried(t *testing.T) {
	updateRetryPause = time.Millisecond
	t.Cleanup(func() { updateRetryPause = 2 * time.Second })
	var tries atomic.Int32
	e := setup(t, func(e *env, d *Deps) {
		d.stars = func(context.Context, int64, string, string, string, int64) error {
			tries.Add(1)
			return billing.ErrBadPayment
		}
	})
	e.tg.push(Update{Message: &Message{MessageID: 9, From: &User{ID: 555}, Chat: Chat{ID: 555, Type: "private"},
		SuccessfulPayment: &SuccessfulPayment{Currency: "XTR", TotalAmount: 10, InvoicePayload: "p", ChargeID: "ch1"}}})
	n := e.tg.count()
	e.say(555, "привет")
	e.tg.wait(t, n, "sendMessage")
	if tries.Load() != 1 {
		t.Fatalf("an unmatched payment was tried %d times", tries.Load())
	}
}

// The outbox hands out a long queue without copying it for every message.
func TestOutboxQueueIsCheapToDrain(t *testing.T) {
	_, c := newSink(t)
	o := NewOutbox(c, Limits{Total: 1e9, Bulk: 1e9, PerChat: 1e9, Burst: 1e9, Workers: 1, MaxTries: 1}, time.Now, nil, nil)
	const jobs = 60000
	for i := range jobs {
		o.Bulk(int64(i), func(context.Context, *Client) error { return nil }, nil)
	}
	start := time.Now()
	for i := range jobs {
		j, _ := o.next()
		if j == nil || j.chat != int64(i) {
			t.Fatalf("job %d: %+v", i, j)
		}
		o.mu.Lock()
		delete(o.busy, j.chat)
		o.mu.Unlock()
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("draining %d jobs took %s", jobs, took)
	}
	if o.pending(prioBulk) != 0 {
		t.Fatal("the lane is not empty")
	}
}

// Run returns only when the poll loop, the outbox and the calls it started have: the
// database is closed after it.
func TestRunWaitsForItsWorkers(t *testing.T) {
	e := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	b := New(e.gate.d)
	returned := make(chan struct{})
	go func() { b.Run(ctx); close(returned) }()
	until(t, "the second bot polls", func() bool { return b.Status().Running })
	if b.out.Load() == nil {
		t.Fatal("no outbox while running")
	}
	cancel()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context ended")
	}
	if b.out.Load() != nil || b.client.Load() != nil {
		t.Fatal("Run returned while the poll loop was still running")
	}
}
