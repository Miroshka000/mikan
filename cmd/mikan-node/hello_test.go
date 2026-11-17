package main

import (
	"context"
	"crypto/x509"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mikan/internal/nodehello"
	"mikan/internal/nodetls"
)

type fakeNode struct {
	mu   sync.Mutex
	url  string
	seen time.Time
}

func (f *fakeNode) PanelURL() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.url
}

func (f *fakeNode) PanelSeen() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seen
}

// While the panel stays away the node says hello now and then, to the panel's address its
// last state named rather than the join key's; once the panel comes, the hellos stop.
func TestSilentHellos(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == nodehello.Path {
			hits.Add(1)
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	pair, err := nodetls.Generate("node-2.mikan", x509.ExtKeyUsageServerAuth, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	key := nodetls.Key{Port: 31234, CertPEM: pair.CertPEM, KeyPEM: pair.KeyPEM, PanelURL: "https://old.example.invalid:2053"}
	n := &fakeNode{url: srv.URL}
	after, most, look := silentAfter, silentMax, silentLook
	silentAfter, silentMax, silentLook = 40*time.Millisecond, 160*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { silentAfter, silentMax, silentLook = after, most, look })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		silentHellos(ctx, key, n, srv.Client(), t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)), time.Now)
		close(done)
	}()
	deadline := time.Now().Add(3 * time.Second)
	for hits.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if hits.Load() < 2 {
		t.Fatalf("hellos while the panel stays away: %d", hits.Load())
	}
	n.mu.Lock()
	n.seen = time.Now().Add(time.Hour) // the panel comes, and keeps coming
	n.mu.Unlock()
	time.Sleep(20 * time.Millisecond)
	before := hits.Load()
	time.Sleep(300 * time.Millisecond)
	if got := hits.Load(); got != before {
		t.Fatalf("hellos while the panel comes: %d more", got-before)
	}
	cancel()
	<-done
}
