package acme

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// The panel reads the address at start; a wrong one is logged and the challenge stays on
// port 80, so certificates keep working where nothing else holds it.
func TestManagerTakesChallengeListenFromEnv(t *testing.T) {
	t.Setenv("MIKAN_ACME_LISTEN", "127.0.0.1:18080")
	if m := New(t.TempDir(), nil, nil, nil, slog.Default(), time.Now); m.challenge.Addr() != "127.0.0.1:18080" {
		t.Fatalf("challenge on %q", m.challenge.Addr())
	}
	var logs bytes.Buffer
	t.Setenv("MIKAN_ACME_LISTEN", "127.0.0.1:99999")
	if m := New(t.TempDir(), nil, nil, nil, slog.New(slog.NewTextHandler(&logs, nil)), time.Now); m.challenge.Addr() != ":80" {
		t.Fatalf("an invalid value: challenge on %q", m.challenge.Addr())
	}
	if !strings.Contains(logs.String(), "MIKAN_ACME_LISTEN") || !strings.Contains(logs.String(), "level=WARN") {
		t.Fatalf("no warning: %s", logs.String())
	}
}
