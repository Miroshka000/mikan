package node

import (
	"os"
	"runtime"
	"runtime/debug"
)

// The node's memory grows with its connections and UDP sessions, and the Go runtime adds to
// that: it lets the heap grow to twice what is alive, whatever the server has, and gives back
// what it freed slowly. On a small server shared with the panel and its database that margin
// is what is missing when the kernel starts to thrash and kills processes, so the runtime
// gets a limit to collect harder near. It is soft: traffic that truly needs more still gets
// it, at the price of more CPU for the collector.

const (
	// memoryShare is the part of the server's RAM the runtime holds itself to: the rest is
	// the kernel's, the panel's and its database's (about half a gigabyte together).
	memoryShare = 0.6

	// The log says how the node's memory stands from memoryLogFrom on, and again each time
	// it grew by memoryLogGrowth since: enough to tell afterwards many connections from few
	// connections and much memory, without a line per tick.
	memoryLogFrom   = 256 << 20
	memoryLogGrowth = 1.25
)

// softLimit is the limit for a server with total bytes of RAM; 0: none known.
func softLimit(total uint64) int64 {
	return int64(float64(total) * memoryShare)
}

// LimitMemory gives the runtime the soft limit and returns it. It returns 0 and leaves the
// runtime as it is when the admin set GOMEMLIMIT or the server's RAM is not known.
func LimitMemory() int64 {
	if os.Getenv("GOMEMLIMIT") != "" {
		return 0
	}
	total, _ := memInfo()
	limit := softLimit(total)
	if limit > 0 {
		debug.SetMemoryLimit(limit)
	}
	return limit
}

// memoryLog says when the memory is worth a line in the log.
type memoryLog struct {
	logged uint64 // the RSS last logged
}

func (m *memoryLog) due(rss uint64) bool {
	// It came back down: the next climb is news again.
	if rss < m.logged/2 {
		m.logged = 0
	}
	if rss < memoryLogFrom || float64(rss) < memoryLogGrowth*float64(m.logged) {
		return false
	}
	m.logged = rss
	return true
}

// TickMemory is called every few seconds from one goroutine.
func (e *Engine) TickMemory() {
	rss := e.sys.last().ProcRSS
	if !e.mem.due(rss) {
		return
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	e.log.Warn("node memory", "rss_mib", rss>>20, "heap_mib", ms.HeapAlloc>>20, "stacks_mib", ms.StackInuse>>20,
		"goroutines", runtime.NumGoroutine(), "conns", e.Reg.ConnCount())
}
