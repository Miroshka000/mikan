// Package webhooks sends the panel's events (package events) to the addresses the admin
// names. An endpoint has the events it is told of, an on switch and a secret, and every
// delivery is a POST of one event, signed (sign.go) and made only to an address the guard
// allows (guard.go). Endpoints follow the feed as an addon that is pushed to does
// (addons/panelapi/push.go): in id order, with a cursor of their own that moves only once the
// receiver answered 2xx, and a delivery that failed is sent again after a pause that doubles
// from a second to five minutes. So delivery is at least once, and a receiver that is down
// for a day is told what it missed, in order, when it is back. What was sent and how it went
// is kept for a week (deliver.go).
package webhooks

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"mikan/internal/panel/events"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

// Limits of what the admin may set.
const (
	// MaxEndpoints bounds the workers and the connections the panel opens for webhooks.
	MaxEndpoints = 20
	MaxName      = 60
)

// TestType is the type of the test event the admin sends by hand. It is not in the feed, so it
// is not one of events.Types and no endpoint can choose it: every test goes to the endpoint
// asked, whatever it is told of.
const TestType = "webhook.test"

// What the admin's requests are refused with, besides the problems of an Input.
var (
	ErrNotFound = errors.New("webhooks: no such endpoint")
	ErrLimit    = errors.New("webhooks: too many endpoints")
)

// Deps is what the service works with.
type Deps struct {
	Store *store.Store
	// Hub wakes the workers when an event is written; nil: they look for events once a second.
	Hub     *events.Hub
	Log     *slog.Logger
	Now     func() time.Time
	Version string // the panel's, in the User-Agent of a delivery
}

// tune lets the tests shorten the waits; the zero value of a field is the real one.
type tune struct{ first, max, scan, timeout time.Duration }

func (t tune) or() tune {
	if t.first == 0 {
		t.first = retryFirst
	}
	if t.max == 0 {
		t.max = retryMax
	}
	if t.scan == 0 {
		t.scan = scanEvery
	}
	if t.timeout == 0 {
		t.timeout = requestTimeout
	}
	return t
}

// Service keeps the endpoints and delivers to them.
type Service struct {
	d     Deps
	tune  tune
	agent string
	// clients are the two ways out: [0] for endpoints that may reach public addresses only,
	// [1] for those that allow local targets.
	clients [2]*http.Client
}

func New(d Deps) *Service { return newService(d, tune{}) }

func newService(d Deps, t tune) *Service {
	t = t.or()
	s := &Service{d: d, tune: t, agent: "mikan-webhooks/" + d.Version}
	s.clients = [2]*http.Client{newClient(false, t.timeout), newClient(true, t.timeout)}
	return s
}

func (s *Service) client(allowLocal bool) *http.Client {
	if allowLocal {
		return s.clients[1]
	}
	return s.clients[0]
}

// Input is what the admin sets of an endpoint.
type Input struct {
	Name       string
	URL        string
	Events     []string // none: every event
	Enabled    bool
	AllowLocal bool
}

// Problem is a field of an Input that cannot be saved. Field is the name the API's body gives
// it (name, url, events), Err one of the errors below, Value what the text may carry (the
// event type that is not one).
type Problem struct {
	Field string
	Err   error
	Value any
}

// ErrNameBlank and ErrEventUnknown are the problems of a name and a choice of events; those
// of the address are guard.go's.
var (
	ErrNameBlank    = errors.New("webhooks: the name is blank")
	ErrEventUnknown = errors.New("webhooks: not an event type")
)

// InvalidError is the refusal of an Input: every field that is wrong, not the first.
type InvalidError struct{ Problems []Problem }

func (e *InvalidError) Error() string {
	var b strings.Builder
	b.WriteString("webhooks: invalid")
	for _, p := range e.Problems {
		fmt.Fprintf(&b, ", %s: %v", p.Field, p.Err)
	}
	return b.String()
}

// normalize cleans in and lists what is wrong with it: the name and address are trimmed, the
// choice of events is put in the order of events.Types without repeats.
func (in *Input) normalize() []Problem {
	var ps []Problem
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		ps = append(ps, Problem{Field: "name", Err: ErrNameBlank})
	}
	in.URL = strings.TrimSpace(in.URL)
	if err := CheckURL(in.URL, in.AllowLocal); err != nil {
		ps = append(ps, Problem{Field: "url", Err: err})
	}
	chosen := make([]string, 0, len(in.Events))
	for _, typ := range in.Events {
		if !events.Known(typ) {
			ps = append(ps, Problem{Field: "events", Err: ErrEventUnknown, Value: typ})
			continue
		}
		if !slices.Contains(chosen, typ) {
			chosen = append(chosen, typ)
		}
	}
	in.Events = make([]string, 0, len(chosen))
	for _, typ := range events.Types() {
		if slices.Contains(chosen, typ) {
			in.Events = append(in.Events, typ)
		}
	}
	return ps
}

// Create makes an endpoint with a secret of its own, which is in the row it returns and is
// not given out again. It starts at the end of the feed: it is told what happens from now on.
func (s *Service) Create(ctx context.Context, in Input) (db.WebhookEndpoint, error) {
	if ps := in.normalize(); len(ps) > 0 {
		return db.WebhookEndpoint{}, &InvalidError{ps}
	}
	var row db.WebhookEndpoint
	err := s.d.Store.Tx(ctx, func(q *db.Queries) error {
		n, err := q.CountWebhooks(ctx)
		if err != nil {
			return err
		}
		if n >= MaxEndpoints {
			return ErrLimit
		}
		row, err = q.CreateWebhook(ctx, db.CreateWebhookParams{Name: in.Name, Url: in.URL, Secret: NewSecret(), Events: in.Events,
			Enabled: in.Enabled, AllowLocal: in.AllowLocal, CreatedAt: s.d.Now().Unix()})
		return err
	})
	return row, err
}

// Patch is a change of an endpoint: what is set, and nothing else.
type Patch struct {
	Name       *string
	URL        *string
	Events     *[]string
	Enabled    *bool
	AllowLocal *bool
}

// Update changes an endpoint and checks it whole as it will be. An endpoint told of more
// types than before does not get the week's old events of the new ones: they start from now,
// unless the endpoint is behind, when what it was owed is still sent (SkipWebhookHistory).
// One that stops asking for a type is not sent the event of it it was retrying.
func (s *Service) Update(ctx context.Context, id int64, p Patch) (db.WebhookEndpoint, error) {
	var row db.WebhookEndpoint
	err := s.d.Store.Tx(ctx, func(q *db.Queries) error {
		cur, err := q.GetWebhook(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		in := Input{Name: cur.Name, URL: cur.Url, Events: cur.Events, Enabled: cur.Enabled, AllowLocal: cur.AllowLocal}
		if p.Name != nil {
			in.Name = *p.Name
		}
		if p.URL != nil {
			in.URL = *p.URL
		}
		if p.Events != nil {
			in.Events = *p.Events
		}
		if p.Enabled != nil {
			in.Enabled = *p.Enabled
		}
		if p.AllowLocal != nil {
			in.AllowLocal = *p.AllowLocal
		}
		if ps := in.normalize(); len(ps) > 0 {
			return &InvalidError{ps}
		}
		if widens(cur.Events, in.Events) {
			if err := q.SkipWebhookHistory(ctx, id); err != nil {
				return err
			}
		}
		row, err = q.UpdateWebhook(ctx, db.UpdateWebhookParams{ID: id, Name: in.Name, Url: in.URL, Events: in.Events,
			Enabled: in.Enabled, AllowLocal: in.AllowLocal})
		if err != nil {
			return err
		}
		_, err = q.FailWebhookRetriesOutside(ctx, db.FailWebhookRetriesOutsideParams{EndpointID: id, Types: in.Events})
		return err
	})
	return row, err
}

// widens says whether the choice to is told of a type that from is not (none: every type).
func widens(from, to []string) bool {
	if len(from) == 0 {
		return false
	}
	return len(to) == 0 || slices.ContainsFunc(to, func(typ string) bool { return !slices.Contains(from, typ) })
}

// Rotate gives an endpoint a new secret, in the row it returns, and the old one stops signing
// at once. What the receiver refuses meanwhile is retried, so nothing is lost while the
// receiver is given the new one.
func (s *Service) Rotate(ctx context.Context, id int64) (db.WebhookEndpoint, error) {
	row, err := s.d.Store.Q.SetWebhookSecret(ctx, db.SetWebhookSecretParams{ID: id, Secret: NewSecret(), SecretAt: s.d.Now().Unix()})
	if errors.Is(err, sql.ErrNoRows) {
		return row, ErrNotFound
	}
	return row, err
}

// Delete removes an endpoint with its log.
func (s *Service) Delete(ctx context.Context, id int64) error {
	n, err := s.d.Store.Q.DeleteWebhook(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Endpoint is an endpoint as the list shows it: the row, how many events wait to be sent to it
// and its newest delivery. The row holds the secret, which no view of it may carry.
type Endpoint struct {
	db.WebhookEndpoint
	Waiting int64
	Last    *db.WebhookDelivery
}

// List gives the endpoints in the order they were made.
func (s *Service) List(ctx context.Context) ([]Endpoint, error) {
	q := s.d.Store.Q
	rows, err := q.ListWebhooks(ctx)
	if err != nil {
		return nil, err
	}
	waiting, err := q.WebhookBacklog(ctx)
	if err != nil {
		return nil, err
	}
	last, err := q.LastWebhookDeliveries(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Endpoint, len(rows))
	for i, r := range rows {
		out[i].WebhookEndpoint = r
		for _, w := range waiting {
			if w.EndpointID == r.ID {
				out[i].Waiting = w.Waiting
			}
		}
		for _, d := range last {
			if d.EndpointID == r.ID {
				out[i].Last = &d
			}
		}
	}
	return out, nil
}

// Deliveries is a page of an endpoint's log, newest first: those before the delivery named
// (0: from the newest), of one status ("": any).
func (s *Service) Deliveries(ctx context.Context, id, before int64, status string, limit int) ([]db.WebhookDelivery, error) {
	if _, err := s.d.Store.Q.GetWebhook(ctx, id); errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	if before == 0 {
		before = 1 << 62
	}
	return s.d.Store.Q.ListWebhookDeliveries(ctx, db.ListWebhookDeliveriesParams{EndpointID: id, Before: before, Status: status, Lim: int64(limit)})
}
