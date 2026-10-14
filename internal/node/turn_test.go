package node

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"mikan/internal/nodeapi"
)

// newTestTurn builds a supervisor with no revive loop, so the tests drive it with Tick.
func newTestTurn() *turnSupervisor {
	return &turnSupervisor{log: slog.New(slog.NewTextHandler(io.Discard, nil)), now: time.Now, revive: time.Hour}
}

// fakeTurnServer is a stand-in for the vk-turn-proxy binary: a shell script, so the tests
// need no network and no real server.
func fakeTurnServer(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("a shell script is not a program here")
	}
	path := filepath.Join(t.TempDir(), "fake-turn")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func waitTurn(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s did not happen in time", what)
}

func TestTurnSupervisorRunsWithTheRightArgsAndStops(t *testing.T) {
	argFile := filepath.Join(t.TempDir(), "args")
	bin := fakeTurnServer(t, `printf '%s\n' "$@" > `+argFile+`
echo listening >&2
sleep 30`)
	s := newTestTurn()
	s.Apply(&nodeapi.Turn{Listen: "0.0.0.0:56000", Target: "127.0.0.1:8443", VLESS: true, Binary: bin})

	st := s.Status()
	if !st.Configured || !st.Running || st.PID == 0 {
		t.Fatalf("expected a running server, got %+v", st)
	}
	waitTurn(t, "the server's argv", func() bool {
		_, err := os.Stat(argFile)
		return err == nil
	})
	raw, err := os.ReadFile(argFile)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-listen", "0.0.0.0:56000", "-connect", "127.0.0.1:8443", "-vless"}
	if got := strings.Fields(string(raw)); !slices.Equal(got, want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}

	// The same state twice must not restart the process.
	pid := st.PID
	s.Apply(&nodeapi.Turn{Listen: "0.0.0.0:56000", Target: "127.0.0.1:8443", VLESS: true, Binary: bin})
	if got := s.Status(); !got.Running || got.PID != pid {
		t.Fatalf("a repeated state restarted the server: %+v", got)
	}

	// Another target restarts it.
	s.Apply(&nodeapi.Turn{Listen: "0.0.0.0:56000", Target: "127.0.0.1:9443", Binary: bin})
	waitTurn(t, "the restart", func() bool {
		got := s.Status()
		return got.Running && got.PID != pid
	})

	// No state stops it.
	s.Apply(nil)
	waitTurn(t, "the stop", func() bool { return !s.Status().Running })
	if st := s.Status(); st.Configured || st.Error != "" {
		t.Fatalf("after a stop: %+v", st)
	}
}

func TestTurnSupervisorReportsACrashAndBacksOff(t *testing.T) {
	bin := fakeTurnServer(t, "echo boom >&2\nexit 3")
	s := newTestTurn()
	s.Apply(&nodeapi.Turn{Listen: "0.0.0.0:56000", Target: "127.0.0.1:8443", Binary: bin})

	waitTurn(t, "the crash", func() bool { return s.Status().Error == "exited" })
	st := s.Status()
	if st.Running || st.Restarts != 1 {
		t.Fatalf("after a crash: %+v", st)
	}
	if !strings.Contains(st.Detail, "boom") {
		t.Fatalf("the reason should be the server's own line, got %q", st.Detail)
	}
	if !slices.Contains(st.Log, "boom") {
		t.Fatalf("the log tail should hold the server's line, got %q", st.Log)
	}

	// The pause holds: a Tick right after the crash does not start it again.
	s.Tick()
	if s.Status().Running {
		t.Fatal("the server was restarted during the backoff")
	}

	// When the pause has passed, the next try happens and the counter grows.
	base := time.Now().Add(turnBackoffMin + time.Second)
	s.now = func() time.Time { return base }
	s.Tick()
	waitTurn(t, "the next try", func() bool { return s.Status().Restarts >= 2 })
}

func TestTurnSupervisorWithoutABinary(t *testing.T) {
	s := newTestTurn()
	s.Apply(&nodeapi.Turn{Listen: "0.0.0.0:56000", Target: "127.0.0.1:8443",
		Binary: filepath.Join(t.TempDir(), "absent")})
	st := s.Status()
	if st.Running || st.Error != "no_binary" || !st.Configured {
		t.Fatalf("a missing binary must be reported, got %+v", st)
	}
	if !strings.Contains(st.Detail, "absent") {
		t.Fatalf("the detail should name the path, got %q", st.Detail)
	}
}

func TestTurnSupervisorRefusesBadConfig(t *testing.T) {
	s := newTestTurn()
	s.Apply(&nodeapi.Turn{Listen: "no-port", Target: "127.0.0.1:8443", Binary: fakeTurnServer(t, "sleep 30")})
	if st := s.Status(); st.Running || st.Error != "bad_config" {
		t.Fatalf("a bad listen address must be refused, got %+v", st)
	}
	s.Apply(&nodeapi.Turn{Listen: "0.0.0.0:56000", Target: ":8443", Binary: fakeTurnServer(t, "sleep 30")})
	if st := s.Status(); st.Running || st.Error != "bad_config" {
		t.Fatalf("a target without a host must be refused, got %+v", st)
	}
}

func TestTurnSinkKeepsTheTail(t *testing.T) {
	var sink turnSink
	for i := 0; i < turnLogLines+5; i++ {
		if _, err := sink.Write([]byte("line\n")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := sink.Write([]byte("last")); err != nil { // no newline yet
		t.Fatal(err)
	}
	if _, err := sink.Write([]byte(" one\n")); err != nil {
		t.Fatal(err)
	}
	tail := sink.tail()
	if len(tail) != turnLogLines {
		t.Fatalf("tail has %d lines, want %d", len(tail), turnLogLines)
	}
	if last := tail[len(tail)-1]; last != "last one" {
		t.Fatalf("last line = %q", last)
	}
}
