package node

import (
	"net/http"
	"net/http/httptest"
	"runtime/debug"
	"testing"
	"time"
)

func TestSoftLimit(t *testing.T) {
	for _, c := range []struct {
		total uint64
		want  int64
	}{
		{0, 0},
		{1 << 30, 644245094},
		{2 << 30, 1288490188},
	} {
		if got := softLimit(c.total); got != c.want {
			t.Errorf("softLimit(%d) = %d, want %d", c.total, got, c.want)
		}
	}
}

// A limit the admin set stays: the node neither replaces nor reports one of its own.
func TestLimitMemoryLeavesTheAdminsOwn(t *testing.T) {
	t.Setenv("GOMEMLIMIT", "123MiB")
	before := debug.SetMemoryLimit(-1)
	if got := LimitMemory(); got != 0 {
		t.Errorf("limit %d over the admin's", got)
	}
	if after := debug.SetMemoryLimit(-1); after != before {
		t.Errorf("runtime limit moved from %d to %d", before, after)
	}
}

// The log follows a climb by steps, not by tick, and a new climb after a fall.
func TestMemoryLogDue(t *testing.T) {
	const mib = 1 << 20
	var m memoryLog
	for _, c := range []struct {
		rss  uint64
		want bool
	}{
		{40 * mib, false},  // the usual
		{255 * mib, false}, // not yet
		{256 * mib, true},  // the first line
		{300 * mib, false},
		{320 * mib, true}, // a quarter more
		{320 * mib, false},
		{399 * mib, false},
		{400 * mib, true},
		{350 * mib, false}, // a little less is nothing
		{199 * mib, false}, // back down, below the threshold
		{256 * mib, true},  // climbing again
	} {
		if got := m.due(c.rss); got != c.want {
			t.Errorf("due(%d MiB) = %v, want %v", c.rss/mib, got, c.want)
		}
	}
}

// The profiles of memory and goroutines are there for whoever reaches the Node API, and
// nothing else of pprof is: the command line and the CPU profile have no business here.
func TestProfilesOnTheNodeAPI(t *testing.T) {
	e := &Engine{dataDir: t.TempDir(), log: quiet(), Reg: NewRegistry("e1", 0, time.Minute, time.Now, &fakeCore{})}
	srv := httptest.NewServer(Handler(e, quiet()))
	defer srv.Close()
	get := func(path string) int {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	for _, path := range []string{"/debug/pprof/heap", "/debug/pprof/goroutine"} {
		if got := get(path); got != http.StatusOK {
			t.Errorf("%s: status %d", path, got)
		}
	}
	for _, path := range []string{"/debug/pprof/", "/debug/pprof/cmdline", "/debug/pprof/profile", "/debug/pprof/symbol", "/debug/pprof/trace"} {
		if got := get(path); got != http.StatusNotFound {
			t.Errorf("%s: status %d, want it absent", path, got)
		}
	}
}
