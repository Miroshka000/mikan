package node

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"mikan/internal/nodeapi"
)

// The node runs the "traffic inside a VK call" server as a child process: the panel says
// what to serve and which port of this node to hand it to, and the binary is the
// third-party vk-turn-proxy baked into the image (see the Dockerfile). The server is an
// ordinary DTLS listener; what makes it a way through a whitelist is the client, which
// reaches it over the TURN servers of VK calls.
//
// A process that dies is brought back with a growing pause; a state without Turn stops
// it. The supervisor never passes secrets to the log: Turn holds none (the VK link lives
// in the client's app).
const (
	// TurnBinary is where the server is looked for unless the admin names another path.
	TurnBinary     = "/usr/local/bin/vk-turn-proxy"
	turnLogLines   = 20               // how many lines of its output the panel may see
	turnRevive     = 10 * time.Second // how often a stopped server is looked at again
	turnBackoffMin = 5 * time.Second
	turnBackoffMax = 5 * time.Minute
	turnShortRun   = 20 * time.Second // a run shorter than this counts as a failure
)

type turnRun struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
	since  time.Time
	done   chan struct{}
}

type turnSupervisor struct {
	log *slog.Logger
	now func() time.Time

	mu        sync.Mutex
	desired   *nodeapi.Turn
	running   *turnRun
	restarts  int
	errCode   string
	errDetail string
	lines     []string  // the tail of the server's output
	nextAt    time.Time // when a restart may be tried (backoff)
	backoff   time.Duration
	revive    time.Duration
}

func newTurnSupervisor(log *slog.Logger) *turnSupervisor {
	s := &turnSupervisor{log: log, now: time.Now, revive: turnRevive}
	go s.loop()
	return s
}

func (s *turnSupervisor) loop() {
	for {
		s.mu.Lock()
		every := s.revive
		s.mu.Unlock()
		if every <= 0 {
			every = turnRevive
		}
		time.Sleep(every)
		s.Tick()
	}
}

// Tick reconciles the running server with the desired state: starts it when it should
// run and does not, and leaves everything alone otherwise.
func (s *turnSupervisor) Tick() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reconcileLocked()
}

// Apply takes the node's new TURN state: nil stops the server, the same state changes
// nothing, another one restarts it.
func (s *turnSupervisor) Apply(t *nodeapi.Turn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case t == nil:
		s.desired = nil
		s.stopLocked()
		s.errCode, s.errDetail = "", ""
		s.backoff, s.nextAt = 0, time.Time{}
	case s.desired != nil && *s.desired == *t:
		s.reconcileLocked()
		return
	default:
		cp := *t
		s.desired = &cp
		s.stopLocked()
		s.backoff, s.nextAt = 0, time.Time{}
		s.reconcileLocked()
	}
}

// Status is the server as it is now, for the panel.
func (s *turnSupervisor) Status() nodeapi.TurnStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := nodeapi.TurnStatus{Configured: s.desired != nil, Restarts: s.restarts, CheckedAt: s.now()}
	if r := s.running; r != nil {
		st.Running, st.PID, st.Since = true, r.cmd.Process.Pid, r.since
	}
	st.Error, st.Detail = s.errCode, s.errDetail
	if len(s.lines) > 0 {
		st.Log = append([]string(nil), s.lines...)
	}
	return st
}

func (s *turnSupervisor) reconcileLocked() {
	if s.desired == nil || s.running != nil || s.now().Before(s.nextAt) {
		return
	}
	s.startLocked(s.desired)
}

func (s *turnSupervisor) startLocked(t *nodeapi.Turn) {
	if _, _, err := net.SplitHostPort(t.Listen); err != nil {
		s.fail("bad_config", "listen: "+err.Error())
		return
	}
	host, _, err := net.SplitHostPort(t.Target)
	if err != nil {
		s.fail("bad_config", "target: "+err.Error())
		return
	}
	if host == "" {
		s.fail("bad_config", "target needs a host")
		return
	}
	bin := t.Binary
	if bin == "" {
		bin = TurnBinary
	}
	if st, err := os.Stat(bin); err != nil || st.IsDir() {
		s.fail("no_binary", bin+" is not on the node: its image is older than this feature")
		return
	}
	args := []string{"-listen", t.Listen, "-connect", t.Target}
	if t.VLESS {
		args = append(args, "-vless")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	sink := &turnSink{}
	cmd.Stdout, cmd.Stderr = sink, sink
	if err := cmd.Start(); err != nil {
		cancel()
		s.fail("failed", err.Error())
		return
	}
	run := &turnRun{cmd: cmd, cancel: cancel, since: s.now(), done: make(chan struct{})}
	s.running = run
	s.errCode, s.errDetail = "", ""
	s.log.Info("turn: server started", "listen", t.Listen, "target", t.Target, "vless", t.VLESS, "pid", cmd.Process.Pid)
	go func() {
		err := cmd.Wait()
		close(run.done)
		s.exited(run, err, sink.tail())
	}()
}

// exited records a process that ended on its own: the growing pause before the next try
// and the reason the admin sees. A process replaced on purpose is not this one anymore
// (s.running points at the new one), so it is ignored.
func (s *turnSupervisor) exited(run *turnRun, err error, tail []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running != run {
		return
	}
	s.running = nil
	s.restarts++
	s.lines = tail
	// What the server last said beats the bare "exit status 3": the admin needs the
	// reason, not the wait(2) code.
	detail := "the server stopped"
	switch {
	case len(tail) > 0:
		detail = tail[len(tail)-1]
	case err != nil:
		detail = err.Error()
	}
	if ran := s.now().Sub(run.since); ran >= turnShortRun {
		s.backoff = turnBackoffMin
	} else {
		s.backoff = min(max(s.backoff*2, turnBackoffMin), turnBackoffMax)
	}
	s.errCode, s.errDetail = "exited", detail
	s.nextAt = s.now().Add(s.backoff)
	s.log.Warn("turn: server stopped", "detail", detail, "retry_in", s.backoff)
}

func (s *turnSupervisor) fail(code, detail string) {
	s.errCode, s.errDetail = code, detail
	s.backoff = min(max(s.backoff*2, turnBackoffMin), turnBackoffMax)
	s.nextAt = s.now().Add(s.backoff)
	s.log.Warn("turn: server not started", "code", code, "detail", detail)
}

// stopLocked asks the process to go away. The exit is handled by the goroutine Wait
// runs; detaching it here first keeps a deliberate stop from counting as a crash.
func (s *turnSupervisor) stopLocked() {
	run := s.running
	if run == nil {
		return
	}
	s.running = nil
	run.cancel()
}

// turnSink keeps the last lines the server wrote, for the admin's status.
type turnSink struct {
	mu    sync.Mutex
	parts []byte
	lines []string
}

func (s *turnSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.parts = append(s.parts, p...)
	for {
		i := bytes.IndexByte(s.parts, '\n')
		if i < 0 {
			break
		}
		s.addLocked(string(s.parts[:i]))
		s.parts = s.parts[i+1:]
	}
	if len(s.parts) > 512 {
		s.addLocked(string(s.parts))
		s.parts = nil
	}
	return len(p), nil
}

func (s *turnSink) addLocked(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	if r := []rune(line); len(r) > 200 {
		line = string(r[:200]) + "…"
	}
	s.lines = append(s.lines, line)
	if len(s.lines) > turnLogLines {
		s.lines = s.lines[len(s.lines)-turnLogLines:]
	}
}

func (s *turnSink) tail() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.lines...)
}
