package tgate

import (
	"container/list"
	"context"
	"errors"
	"sync"
	"time"
)

// The outbox is the only way the bot talks to chats. Telegram allows about one message a
// second in a chat (short bursts pass) and about 30 a second in all; beyond that it
// answers 429 with how long to wait, and a bot that keeps pushing risks its limits.
//
//   - A reply to someone looking at the chat goes first; notices next; a broadcast gets
//     what is left, below the total, so it never makes a reply wait.
//   - A newer screen for the same menu message replaces an older one not yet sent: a
//     user tapping fast sees the last screen, at most one edit a second, and no error.
//   - A 429 is waited out and the message sent then; nothing is dropped for it.
//   - A chat's messages go out in order, one at a time.

type priority int

const (
	prioReply  priority = iota // the user is in the chat and waits
	prioNotice                 // a notification about their subscription
	prioBulk                   // a broadcast
	prioCount
)

// Limits of the outbox; tests make them faster.
type Limits struct {
	Total    float64 // messages a second in all
	Bulk     float64 // notices and broadcasts, a part of Total
	PerChat  float64 // messages a second in one chat
	Burst    float64 // a chat's short burst: a message, its old menu gone, a new one sent
	Workers  int     // requests in flight
	MaxTries int     // a message that keeps failing is given up after this many sends
}

// DefaultLimits keep under Telegram's: 28 of ~30 a second, broadcasts at 20 so replies
// always have room, one a second in a chat after a burst of three.
var DefaultLimits = Limits{Total: 28, Bulk: 20, PerChat: 1, Burst: 3, Workers: 4, MaxTries: 5}

// job is one piece of work for one chat: a send, an edit, or a few calls in order.
type job struct {
	chat int64
	prio priority
	key  string // a newer job with the same key replaces this one while it waits
	cost float64
	do   func(ctx context.Context, c *Client) error
	done func(error) // nil or called once: sent, given up, or dropped at stop

	tries     int
	notBefore time.Time
}

// bucket is a token bucket: rate tokens a second, at most burst stored.
type bucket struct {
	rate, burst, tokens float64
	last                time.Time
}

func newBucket(rate, burst float64, now time.Time) *bucket {
	return &bucket{rate: rate, burst: burst, tokens: burst, last: now}
}

func (b *bucket) refill(now time.Time) {
	if now.After(b.last) {
		b.tokens = min(b.burst, b.tokens+b.rate*now.Sub(b.last).Seconds())
		b.last = now
	}
}

// wait is how long until n tokens are there (0: now).
func (b *bucket) wait(now time.Time, n float64) time.Duration {
	b.refill(now)
	if b.tokens >= n {
		return 0
	}
	return time.Duration((n - b.tokens) / b.rate * float64(time.Second))
}

func (b *bucket) take(n float64) { b.tokens -= n }

// Outbox runs the sends of one bot token.
type Outbox struct {
	c    *Client
	lim  Limits
	now  func() time.Time
	log  func(error)
	gone func(chat int64) // the user blocked the bot

	mu       sync.Mutex
	queues   [prioCount]*list.List // of *job, the first to go at the front
	byKey    map[string]*job
	chats    map[int64]*bucket
	busy     map[int64]bool // a job of the chat is being sent
	total    *bucket
	bulk     *bucket
	paused   time.Time           // a 429 without a chat in it stops everything until then
	chatOff  map[int64]time.Time // a 429 on a reply stops only that chat
	wake     chan struct{}
	stopped  chan struct{} // closed when Run ends
	stopped1 sync.Once
}

func NewOutbox(c *Client, lim Limits, now func() time.Time, gone func(int64), logErr func(error)) *Outbox {
	t := now()
	o := &Outbox{c: c, lim: lim, now: now, log: logErr, gone: gone, byKey: map[string]*job{}, chats: map[int64]*bucket{},
		busy: map[int64]bool{}, total: newBucket(lim.Total, lim.Total, t), bulk: newBucket(lim.Bulk, lim.Bulk, t),
		chatOff: map[int64]time.Time{}, wake: make(chan struct{}, 1), stopped: make(chan struct{})}
	for p := range o.queues {
		o.queues[p] = list.New()
	}
	return o
}

func (o *Outbox) poke() {
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

// add queues a job; with a key it replaces a waiting job with the same key in place.
func (o *Outbox) add(j *job) {
	if j.cost == 0 {
		j.cost = 1
	}
	o.mu.Lock()
	if j.key != "" {
		if old, ok := o.byKey[j.key]; ok {
			// Keep the older one's place: the chat waited for it already.
			oldDone := old.done
			*old = *j
			o.byKey[j.key] = old
			o.mu.Unlock()
			if oldDone != nil {
				oldDone(nil)
			}
			o.poke()
			return
		}
		o.byKey[j.key] = j
	}
	o.queues[j.prio].PushBack(j)
	o.mu.Unlock()
	o.poke()
}

// Reply sends to a user who waits in the chat. key merges successive screens of one menu.
func (o *Outbox) Reply(chat int64, key string, cost float64, do func(context.Context, *Client) error) {
	o.add(&job{chat: chat, prio: prioReply, key: key, cost: cost, do: do})
}

// Notice sends a notification.
func (o *Outbox) Notice(chat int64, do func(context.Context, *Client) error, done func(error)) {
	o.add(&job{chat: chat, prio: prioNotice, do: do, done: done})
}

// Bulk sends a broadcast message.
func (o *Outbox) Bulk(chat int64, do func(context.Context, *Client) error, done func(error)) {
	o.add(&job{chat: chat, prio: prioBulk, do: do, done: done})
}

// Pending is how many messages wait of a priority or below it (prioCount: all).
func (o *Outbox) pending(p priority) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := 0
	for i := p; i < prioCount; i++ {
		n += o.queues[i].Len()
	}
	return n
}

// BulkPending is how many broadcast messages wait: a broadcast is fed in as they go.
func (o *Outbox) BulkPending() int { return o.pending(prioBulk) }

// next takes the first job allowed to go now, by priority, and its tokens; otherwise it
// says how long until something could.
func (o *Outbox) next() (*job, time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	now := o.now()
	wait := time.Second
	if now.Before(o.paused) {
		return nil, o.paused.Sub(now)
	}
	for p := range prioCount {
		for e := o.queues[p].Front(); e != nil; e = e.Next() {
			j := e.Value.(*job)
			if o.busy[j.chat] || now.Before(j.notBefore) {
				if now.Before(j.notBefore) {
					wait = min(wait, j.notBefore.Sub(now))
				}
				continue
			}
			if off := o.chatOff[j.chat]; now.Before(off) {
				wait = min(wait, off.Sub(now))
				continue
			}
			cb := o.chats[j.chat]
			if cb == nil {
				cb = newBucket(o.lim.PerChat, o.lim.Burst, now)
				o.chats[j.chat] = cb
			}
			w := max(cb.wait(now, j.cost), o.total.wait(now, j.cost))
			if p != prioReply {
				w = max(w, o.bulk.wait(now, j.cost))
			}
			if w > 0 {
				wait = min(wait, w)
				if p != prioReply && o.total.wait(now, j.cost) > 0 {
					break // the whole lane waits for the total; later jobs would too
				}
				continue
			}
			cb.take(j.cost)
			o.total.take(j.cost)
			if p != prioReply {
				o.bulk.take(j.cost)
			}
			o.queues[p].Remove(e)
			if j.key != "" && o.byKey[j.key] == j {
				delete(o.byKey, j.key)
			}
			o.busy[j.chat] = true
			return j, 0
		}
	}
	return nil, max(wait, time.Millisecond)
}

// Run sends until ctx ends; what is still queued then is dropped.
func (o *Outbox) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for range max(1, o.lim.Workers) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o.work(ctx)
		}()
	}
	wg.Wait()
	o.mu.Lock()
	var left []*job
	for p := range o.queues {
		for e := o.queues[p].Front(); e != nil; e = e.Next() {
			left = append(left, e.Value.(*job))
		}
		o.queues[p].Init()
	}
	o.byKey = map[string]*job{}
	o.mu.Unlock()
	o.stopped1.Do(func() { close(o.stopped) })
	for _, j := range left {
		if j.done != nil {
			j.done(context.Canceled)
		}
	}
}

// Stopped is closed once Run has ended and dropped what was queued.
func (o *Outbox) Stopped() <-chan struct{} { return o.stopped }

func (o *Outbox) work(ctx context.Context) {
	for ctx.Err() == nil {
		j, wait := o.next()
		if j == nil {
			t := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-o.wake:
				t.Stop()
			case <-t.C:
			}
			continue
		}
		err := j.do(ctx, o.c)
		o.finish(j, err)
		o.poke() // the chat is free for its next job
	}
}

func (o *Outbox) finish(j *job, err error) {
	var ae *APIError
	now := o.now()
	o.mu.Lock()
	delete(o.busy, j.chat)
	retry := false
	switch {
	case err == nil:
	case errors.As(err, &ae) && ae.Code == 429:
		// Telegram says how long; a reply's flood is the chat's, the rest the bot's.
		after := max(ae.RetryAfter, time.Second)
		if j.prio == prioReply {
			o.chatOff[j.chat] = now.Add(after)
		} else if now.Add(after).After(o.paused) {
			o.paused = now.Add(after)
		}
		retry = true
	case errors.As(err, &ae):
		// Refused for good: blocked, chat gone, a bad request. Sending again changes nothing.
	case errors.Is(err, context.Canceled):
	default:
		// The network: again in a moment, a few times.
		j.tries++
		j.notBefore = now.Add(time.Duration(j.tries) * 2 * time.Second)
		retry = j.tries < o.lim.MaxTries
	}
	superseded := false
	if retry && j.key != "" {
		// A newer screen for the same message came while this one was out: that one goes.
		if _, taken := o.byKey[j.key]; taken {
			retry, superseded = false, true
		} else {
			o.byKey[j.key] = j
		}
	}
	if retry {
		// Back to the head of its lane: it keeps its turn.
		o.queues[j.prio].PushFront(j)
	}
	for chat, t := range o.chatOff {
		if !now.Before(t) {
			delete(o.chatOff, chat)
		}
	}
	if len(o.chats) > 5000 {
		// Chats idle long enough have full buckets again; forget them.
		for chat, b := range o.chats {
			if b.wait(now, b.burst) == 0 && !o.busy[chat] {
				delete(o.chats, chat)
			}
		}
	}
	o.mu.Unlock()
	if retry {
		return
	}
	if superseded {
		err = nil
	}
	if errors.As(err, &ae) && ae.Code == 403 && o.gone != nil {
		o.gone(j.chat)
	} else if err != nil && o.log != nil && !errors.Is(err, context.Canceled) {
		o.log(err)
	}
	if j.done != nil {
		j.done(err)
	}
}

// Do runs a call of a bot addon in its chat's turn, at the level it asked for ("notice",
// "bulk", else a reply), and calls done once with how it ended: the Bot API proxy waits for
// Telegram's own answer, so nothing is merged by key here.
func (o *Outbox) Do(chat int64, level string, do func(context.Context, *Client) error, done func(error)) {
	prio := prioReply
	switch level {
	case "notice":
		prio = prioNotice
	case "bulk":
		prio = prioBulk
	}
	o.add(&job{chat: chat, prio: prio, do: do, done: done})
}
