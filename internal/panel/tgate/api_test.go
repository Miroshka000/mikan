package tgate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A message Telegram cannot parse goes again as plain text instead of failing; any other
// refusal stays an error.
func TestSendFallsBackToPlainText(t *testing.T) {
	var calls []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		calls = append(calls, body)
		switch {
		case body["parse_mode"] == "HTML":
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": 400, "description": "Bad Request: can't parse entities: Unsupported start tag \"x\" at byte offset 0"})
		case strings.HasSuffix(r.URL.Path, "/editMessageText"):
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": true})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 5, "chat": map[string]any{"id": 1, "type": "private"}}})
		}
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "1:x", nil)
	m, err := c.Send(context.Background(), 1, "<x>a &lt; b</x>", nil, false)
	if err != nil || m.MessageID != 5 || len(calls) != 2 || calls[1]["text"] != "a < b" || calls[1]["parse_mode"] != nil {
		t.Fatalf("send: %+v %v %v", m, err, calls)
	}
	calls = nil
	if err := c.Edit(context.Background(), 1, 5, "<x>c</x>", nil); err != nil || len(calls) != 2 || calls[1]["text"] != "c" {
		t.Fatalf("edit: %v %v", err, calls)
	}

	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": 403, "description": "Forbidden: bot was blocked by the user"})
	}))
	defer other.Close()
	if _, err := NewClient(other.URL, "1:x", nil).Send(context.Background(), 1, "a", nil, false); err == nil {
		t.Fatal("a blocked bot is not a parse error")
	}
}

// PlainText is what is left of the HTML when Telegram refuses it: the text, unescaped.
func TestPlainText(t *testing.T) {
	if got := PlainText(`<b>a &lt; b</b> <a href="https://e.com">c &amp; d</a> &#34;e&#34;`); got != `a < b c & d "e"` {
		t.Fatalf("plain %q", got)
	}
}
