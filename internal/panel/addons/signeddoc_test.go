package addons

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// A caller that leaves while the document is fetched (the admin left the page before GitHub
// answered) does not fail the fetch: it goes on, the next caller gets the document from it,
// and the cancellation is not kept as the network's failure.
func TestSignedDocCallerLeaves(t *testing.T) {
	d := signedCache[int]{name: "doc", updated: func(int) time.Time { return time.Time{} }}
	now := time.Unix(1000, 0)
	answer := make(chan struct{})
	var fetches atomic.Int32
	fetch := func(ctx context.Context) (int, error) {
		fetches.Add(1)
		select {
		case <-answer:
			return 7, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	ctx, leave := context.WithCancel(context.Background())
	leave()
	if _, err := d.get(ctx, now, "", fetch); !errors.Is(err, context.Canceled) {
		t.Fatalf("the caller that left: %v", err)
	}
	close(answer)
	if v, err := d.get(context.Background(), now, "", fetch); err != nil || v != 7 {
		t.Fatalf("the next caller: %d %v", v, err)
	}
	if n := fetches.Load(); n != 1 {
		t.Fatalf("fetched %d times: the first fetch serves both callers", n)
	}
}

// A failure of the network is kept for docRetry, so the pages do not wait on GitHub each
// time; the copy at hand, once there is one, stands in for the document meanwhile.
func TestSignedDocFailureKept(t *testing.T) {
	d := signedCache[int]{name: "doc", updated: func(int) time.Time { return time.Time{} }}
	now := time.Unix(1000, 0)
	down := errors.New("github is down")
	var fetches atomic.Int32
	fail := func(context.Context) (int, error) { fetches.Add(1); return 0, down }
	ok := func(context.Context) (int, error) { fetches.Add(1); return 7, nil }
	for range 2 {
		if _, err := d.get(context.Background(), now, "", fail); !errors.Is(err, down) {
			t.Fatalf("no copy at hand: %v", err)
		}
	}
	if n := fetches.Load(); n != 1 {
		t.Fatalf("fetched again within docRetry: %d", n)
	}
	if v, err := d.get(context.Background(), now.Add(docRetry), "", ok); err != nil || v != 7 {
		t.Fatalf("after docRetry: %d %v", v, err)
	}
	if v, err := d.get(context.Background(), now.Add(docRetry+docFresh), "", fail); err != nil || v != 7 {
		t.Fatalf("the copy at hand: %d %v", v, err)
	}
}
