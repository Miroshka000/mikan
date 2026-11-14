package addons

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"mikan/internal/fsutil"
)

// The signed documents the panel reads from the network (the official catalog, the
// registry of community addons): one cache for both.

const (
	// docFresh is how long a document is used before it is fetched again.
	docFresh = 10 * time.Minute
	// docRetry is how long after a failed fetch the network is left alone: the copy at hand
	// is used meanwhile, and the pages that read it do not wait on GitHub each time.
	docRetry = time.Minute
)

// ErrRolledBack refuses a document older than one the panel has seen: an index put back
// from before would bring back an addon the registry revoked since.
var ErrRolledBack = errors.New("addons: the signed document is older than the one seen before")

// signedCache keeps one signed document between fetches. One fetch runs at a time and every
// caller waits for it; a failed fetch gives the copy at hand, if any. The newest "updated" seen
// is kept in a file (seen) across restarts, so a replayed document is refused after one too.
type signedCache[T any] struct {
	name    string // in the seen file
	updated func(T) time.Time

	mu     sync.Mutex
	val    *T
	at     time.Time // when val was fetched
	failAt time.Time // when the last fetch failed
	err    error     // why
	flight chan struct{}
}

// get is the document: the one at hand while it is fresh (or the network failed lately),
// else a fresh one from fetch. seen is the file that keeps the newest dates, "" none.
//
// The fetch does not belong to the caller that started it, as a source's does not
// (sources.go): a caller that leaves (the admin left the page before GitHub answered) stops
// waiting, the fetch goes on for the others and the next ask, and a cancellation is never
// kept as the network's failure. The client's own timeout bounds it.
func (d *signedCache[T]) get(ctx context.Context, now time.Time, seen string, fetch func(context.Context) (T, error)) (T, error) {
	for {
		d.mu.Lock()
		switch {
		case d.val != nil && now.Sub(d.at) < docFresh:
			v := *d.val
			d.mu.Unlock()
			return v, nil
		case d.err != nil && now.Sub(d.failAt) < docRetry:
			v, err := d.stale()
			d.mu.Unlock()
			return v, err
		case d.flight == nil:
			d.flight = make(chan struct{})
			go d.refresh(context.WithoutCancel(ctx), now, seen, fetch, d.flight)
		}
		wait := d.flight
		d.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			var zero T
			return zero, ctx.Err()
		}
	}
}

// refresh runs one fetch for whoever waits on done and keeps how it went: the document, or
// the failure, which the copy at hand covers for docRetry.
func (d *signedCache[T]) refresh(ctx context.Context, now time.Time, seen string, fetch func(context.Context) (T, error), done chan struct{}) {
	v, err := fetch(ctx)
	if err == nil {
		err = d.newer(seen, d.updated(v))
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if err != nil {
		d.err, d.failAt = err, now
	} else {
		d.val, d.at, d.err = &v, now, nil
	}
	d.flight = nil
	close(done)
}

// stale is the copy at hand after a failed fetch: no error with one, the failure without.
// The caller holds mu.
func (d *signedCache[T]) stale() (T, error) {
	if d.val != nil {
		return *d.val, nil
	}
	var zero T
	return zero, d.err
}

// known is the document fetched last, however old; nil before the first fetch.
func (d *signedCache[T]) known() *T {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.val == nil {
		return nil
	}
	v := *d.val
	return &v
}

// reset forgets the document (another address to read it from).
func (d *signedCache[T]) reset() {
	d.mu.Lock()
	d.val, d.err = nil, nil
	d.mu.Unlock()
}

// seenMu orders the writes of the seen file between documents.
var seenMu sync.Mutex

// newer refuses a date older than the newest seen for the document and keeps a newer one.
// The file is a small JSON object of dates by name; one that cannot be read starts over.
func (d *signedCache[T]) newer(seen string, updated time.Time) error {
	d.mu.Lock()
	if d.val != nil && updated.Before(d.updated(*d.val)) {
		d.mu.Unlock()
		return ErrRolledBack
	}
	d.mu.Unlock()
	if seen == "" {
		return nil
	}
	seenMu.Lock()
	defer seenMu.Unlock()
	dates := map[string]time.Time{}
	if raw, err := os.ReadFile(seen); err == nil {
		_ = json.Unmarshal(raw, &dates)
	}
	if updated.Before(dates[d.name]) {
		return ErrRolledBack
	}
	if !updated.After(dates[d.name]) {
		return nil
	}
	dates[d.name] = updated
	raw, err := json.Marshal(dates)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(seen), 0o700); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(seen, raw, 0o600)
}
