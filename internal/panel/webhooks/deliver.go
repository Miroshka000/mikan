package webhooks

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"mikan/internal/pace"
	"mikan/internal/panel/events"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

// Delivery follows the push to addons (addons/panelapi/push.go) in everything but the shape
// of a request. A worker lives for each endpoint that is on, reads the feed after the
// endpoint's cursor, in batches of at most batch events and in id order, and posts the events
// one by one, each as its own request: a receiver of a webhook reads one event, not a list.
// One request is in flight per endpoint, so the events come in order, and the cursor moves
// after each 2xx and never before. An event the receiver did not take (anything but 2xx, a
// redirect among them, or no answer) is sent again after a pause that doubles from a second to
// five minutes and starts over after a success; the events behind it wait.
const (
	retryFirst = time.Second
	retryMax   = 5 * time.Minute
	// scanEvery is how often the supervisor looks for endpoints that came on or were made.
	scanEvery  = 5 * time.Second
	batch      = 100
	pruneEvery = 10 * time.Minute
)

// LogRetention is how long a row of the log is kept after its last attempt: as long as the
// feed keeps an event, so an event that is still being sent has its row.
const LogRetention = events.Retention

// The states of a delivery in the log (webhook_deliveries.status).
const (
	statusRetrying  = "retrying"
	statusDelivered = "delivered"
	statusFailed    = "failed"
)

// Result is how one attempt went.
type Result struct {
	OK      bool
	Status  int           // the HTTP status of the answer; 0 when there was none
	Failure string        // a Fail* code; "" after a 2xx
	Took    time.Duration // from the request to the end of the answer that was read
}

// post sends one event to the endpoint, once. It signs it afresh, so the timestamp is that of
// this attempt, and reads at most maxAnswer bytes of the answer: nothing of it is kept, only
// the status.
func (s *Service) post(ctx context.Context, ep db.WebhookEndpoint, ev events.Event) Result {
	body, err := json.Marshal(ev)
	if err != nil {
		s.d.Log.Error("webhooks: an event does not encode", "event", ev.ID, "err", err)
		return Result{Failure: FailInternal}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.Url, bytes.NewReader(body))
	if err != nil {
		// What is saved was parsed before, so this is a bug here, not the admin's address.
		s.d.Log.Error("webhooks: a saved address does not make a request", "endpoint", ep.ID, "err", err)
		return Result{Failure: FailInternal}
	}
	req.Header.Set("User-Agent", s.agent)
	setHeaders(req.Header, ep.Secret, ev, s.d.Now(), body)
	start := time.Now()
	resp, err := s.client(ep.AllowLocal).Do(req)
	if err != nil {
		return Result{Failure: failure(err), Took: time.Since(start)}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxAnswer))
	res := Result{Status: resp.StatusCode, Took: time.Since(start)}
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		res.OK = true
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		res.Failure = FailRedirect
	default:
		res.Failure = FailStatus
	}
	return res
}

// errGone: the endpoint was deleted while an event was on its way to it.
var errGone = errors.New("webhooks: the endpoint is gone")

// eventOf is a row of the feed as the receiver gets it: the envelope the addons' feed has.
func eventOf(r db.AddonEvent) events.Event {
	return events.Event{ID: r.ID, Type: r.Type, At: store.Time(r.CreatedAt), UserID: r.UserID.Int64, TgID: r.TgID.Int64, Data: json.RawMessage(r.Data)}
}

// send makes one attempt at a feed event and writes it to the log; after a 2xx, in the same
// transaction, the endpoint's cursor moves to the event. An error is not the receiver's
// refusal (that is in the Result) but what stopped the attempt being written down, or ctx: the
// event is then sent again, which delivery being at least once allows.
func (s *Service) send(ctx context.Context, ep db.WebhookEndpoint, ev events.Event) (Result, error) {
	res := s.post(ctx, ep, ev)
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	status := statusRetrying
	if res.OK {
		status = statusDelivered
	}
	err := s.d.Store.Tx(ctx, func(q *db.Queries) error {
		err := q.RecordWebhookAttempt(ctx, db.RecordWebhookAttemptParams{EndpointID: ep.ID, EventID: sql.NullInt64{Int64: ev.ID, Valid: true},
			Type: ev.Type, Status: status, ResponseCode: int64(res.Status), Error: res.Failure, CreatedAt: s.d.Now().Unix()})
		if err != nil {
			return err
		}
		if res.OK {
			// Only now: an event the receiver did not take is still ahead of the cursor.
			return q.AckWebhook(ctx, db.AckWebhookParams{ID: ep.ID, LastID: ev.ID})
		}
		// The admin may have narrowed the endpoint's choice while the request was out, after
		// the edit ended the retries it knew of: the row of an event the endpoint no longer asks
		// for is not one that will be tried again.
		cur, err := q.GetWebhook(ctx, ep.ID)
		if err != nil {
			return err
		}
		_, err = q.FailWebhookRetriesOutside(ctx, db.FailWebhookRetriesOutsideParams{EndpointID: ep.ID, Types: cur.Events})
		return err
	})
	if store.IsForeignKey(err) || errors.Is(err, sql.ErrNoRows) {
		err = errGone
	}
	return res, err
}

// Test sends the test event to an endpoint once, whether it is on or not and whatever events
// it is told of, and says how it went. The attempt is in the endpoint's log as a row of its
// own; a test is not tried again.
func (s *Service) Test(ctx context.Context, id int64) (Result, error) {
	ep, err := s.d.Store.Q.GetWebhook(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return Result{}, ErrNotFound
	} else if err != nil {
		return Result{}, err
	}
	data, err := json.Marshal(struct {
		EndpointID int64  `json:"endpoint_id"`
		Message    string `json:"message"`
	}{id, "A test event from the mikan panel: nothing happened."})
	if err != nil {
		return Result{}, err
	}
	now := s.d.Now()
	res := s.post(ctx, ep, events.Event{Type: TestType, At: store.Time(now.Unix()), Data: data})
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	status := statusFailed
	if res.OK {
		status = statusDelivered
	}
	err = s.d.Store.Q.RecordWebhookAttempt(ctx, db.RecordWebhookAttemptParams{EndpointID: id, Type: TestType, Status: status,
		ResponseCode: int64(res.Status), Error: res.Failure, CreatedAt: now.Unix()})
	if store.IsForeignKey(err) {
		err = ErrNotFound
	}
	return res, err
}

// Run delivers until ctx ends: it keeps a worker for each endpoint that is on, and prunes the
// log. It returns once every worker has stopped.
func (s *Service) Run(ctx context.Context) {
	var (
		mu      sync.Mutex
		workers = map[int64]bool{}
		wg      sync.WaitGroup
	)
	defer wg.Wait()
	scan := func(ctx context.Context) {
		rows, err := s.d.Store.Q.ListWebhooks(ctx)
		if err != nil {
			if ctx.Err() == nil {
				s.d.Log.Warn("webhooks: endpoints", "err", err)
			}
			return
		}
		for _, ep := range rows {
			if !ep.Enabled {
				continue
			}
			mu.Lock()
			busy := workers[ep.ID]
			workers[ep.ID] = true
			mu.Unlock()
			if busy {
				continue
			}
			wg.Go(func() {
				s.follow(ctx, ep.ID)
				mu.Lock()
				delete(workers, ep.ID)
				mu.Unlock()
			})
		}
	}
	wg.Go(func() { pace.Tick(ctx, pruneEvery, s.prune) })
	scan(ctx)
	pace.Tick(ctx, s.tune.scan, scan)
}

// prune ends the rows of events that left the feed while they were being tried, and drops
// the rows nothing was tried on for LogRetention.
func (s *Service) prune(ctx context.Context) {
	q := s.d.Store.Q
	if _, err := q.FailLostWebhookDeliveries(ctx); err != nil && ctx.Err() == nil {
		s.d.Log.Warn("webhooks: log", "err", err)
	}
	if _, err := q.PruneWebhookDeliveries(ctx, s.d.Now().Add(-LogRetention).Unix()); err != nil && ctx.Err() == nil {
		s.d.Log.Warn("webhooks: log", "err", err)
	}
}

// follow delivers to one endpoint until it is deleted or turned off, or ctx ends.
func (s *Service) follow(ctx context.Context, id int64) {
	backoff := pace.Backoff{Base: s.tune.first, Max: s.tune.max}
	fails := 0 // failures in a row: 0 after a success
	// failed counts a failure and gives how long it waits.
	failed := func() time.Duration {
		fails++
		return backoff.Next(fails - 1)
	}
	for ctx.Err() == nil {
		ep, ok := s.live(ctx, id)
		if !ok {
			return
		}
		rows, err := s.next(ctx, ep)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			s.d.Log.Warn("webhooks: events", "endpoint", id, "err", err)
			alive, edited := s.pause(ctx, ep, failed())
			if !alive {
				return
			}
			if edited {
				fails = 0
			}
			continue
		}
		for _, row := range rows {
			// The endpoint as it is now: an address fixed, a secret rotated, or the endpoint
			// turned off, since the last event.
			if ep, ok = s.live(ctx, id); !ok {
				return
			}
			ev := eventOf(row)
			res, err := s.send(ctx, ep, ev)
			if errors.Is(err, errGone) || ctx.Err() != nil {
				return
			}
			if err == nil && res.OK {
				fails = 0
				continue
			}
			pause := failed()
			if err != nil {
				s.d.Log.Warn("webhooks: a delivery was not written down, sending again", "endpoint", id, "event", ev.ID, "after", pause, "err", err)
			} else {
				s.d.Log.Warn("webhooks: the receiver did not take an event, sending again", "endpoint", id, "event", ev.ID, "type", ev.Type,
					"failure", res.Failure, "status", res.Status, "after", pause)
			}
			alive, edited := s.pause(ctx, ep, pause)
			if !alive {
				return
			}
			if edited {
				fails = 0
			}
			// The failed event is first in the next read, and the ones behind it follow it.
			break
		}
	}
}

// live is the endpoint as it is now, when there is a worker's work for it: it is there and on.
func (s *Service) live(ctx context.Context, id int64) (db.WebhookEndpoint, bool) {
	ep, err := s.d.Store.Q.GetWebhook(ctx, id)
	switch {
	case err == nil:
		return ep, ep.Enabled
	case errors.Is(err, sql.ErrNoRows) || ctx.Err() != nil:
		return ep, false
	}
	// The database did not answer: the worker ends, and the next scan starts it again.
	s.d.Log.Warn("webhooks: endpoint", "endpoint", id, "err", err)
	return ep, false
}

// next is the events the endpoint is owed: after its cursor, of the types it is told of.
// When there are none it waits for a kick of the hub for at most a second (a kick may come
// before the commit it announces, so it does not wait longer), and the worker asks again.
func (s *Service) next(ctx context.Context, ep db.WebhookEndpoint) ([]db.AddonEvent, error) {
	// Taken before the read: what is committed after the read kicks it.
	woken := s.d.Hub.Wait()
	rows, err := s.d.Store.Q.ListAddonEvents(ctx, db.ListAddonEventsParams{After: ep.LastID, Types: ep.Events, Lim: batch})
	if err != nil || len(rows) > 0 {
		return rows, err
	}
	t := time.NewTimer(time.Second)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-woken:
	case <-t.C:
	}
	return nil, nil
}

// pause waits d, in slices of a second, and says whether the worker goes on: not when ctx
// ended or the endpoint was deleted or turned off, so a pause of five minutes does not outlive
// it. A change of where or how it is sent (the address, the secret, what local it may reach)
// ends the wait early with edited true: whoever fixed the address wants the next try now, not
// in five minutes.
func (s *Service) pause(ctx context.Context, ep db.WebhookEndpoint, d time.Duration) (alive, edited bool) {
	deadline := time.Now().Add(d)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			return true, false
		}
		if !pace.Sleep(ctx, min(left, time.Second)) {
			return false, false
		}
		now, ok := s.live(ctx, ep.ID)
		if !ok {
			return false, false
		}
		if now.Url != ep.Url || now.Secret != ep.Secret || now.AllowLocal != ep.AllowLocal {
			return true, true
		}
	}
}
