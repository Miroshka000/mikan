package tgbot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"mikan/internal/panel/tgate"
)

// sink is a Bot API that records the messages sent, for the outbox the broadcasts feed.
type sink struct {
	mu   sync.Mutex
	sent []int64
}

func (s *sink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ChatID int64 `json:"chat_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	s.sent = append(s.sent, body.ChatID)
	n := len(s.sent)
	s.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": n, "chat": map[string]any{"id": body.ChatID}}})
}

// wait until n messages have gone out.
func (s *sink) wait(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		got := len(s.sent)
		s.mu.Unlock()
		if got >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("not all %d sent", n)
}

var fast = tgate.Limits{Total: 1000, Bulk: 1000, PerChat: 1000, Burst: 1000, Workers: 1, MaxTries: 3}

func newSink(t *testing.T) (*sink, *tgate.Client) {
	t.Helper()
	s := &sink{}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	return s, tgate.NewClient(srv.URL, "1:x", nil)
}

func say(chat int64, text string) func(context.Context, *tgate.Client) error {
	return func(ctx context.Context, c *tgate.Client) error {
		_, err := c.Send(ctx, chat, text, nil, false)
		return err
	}
}

func run(t *testing.T, o *tgate.Outbox) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go o.Run(ctx)
}
