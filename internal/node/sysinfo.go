package node

import (
	"bufio"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"mikan/internal/nodeapi"
)

// sysSampler reads host-wide CPU, memory and network counters from /proc. With
// network_mode: host, /proc/stat and /proc/net/dev describe the whole server.
type sysSampler struct {
	mu  sync.Mutex
	cur nodeapi.System
}

func newSysSampler() *sysSampler { return &sysSampler{} }

func (s *sysSampler) last() nodeapi.System {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}

func (s *sysSampler) run() {
	const every = 2 * time.Second
	prevIdle, prevTotal, _ := cpuTimes()
	prevProc, _ := procCPUTime()
	prevRx, prevTx, _ := netBytes()
	t := time.NewTicker(every)
	defer t.Stop()
	for range t.C {
		var cur nodeapi.System
		idle, total, errCPU := cpuTimes()
		proc, errProc := procCPUTime()
		if errCPU == nil && total > prevTotal {
			cur.CPUPercent = 100 * (1 - float64(idle-prevIdle)/float64(total-prevTotal))
			// Both in clock ticks, /proc/stat's summed over all cores: the same scale.
			if errProc == nil && proc >= prevProc {
				cur.ProcCPUPercent = min(100, 100*float64(proc-prevProc)/float64(total-prevTotal))
			}
		}
		prevIdle, prevTotal, prevProc = idle, total, proc
		cur.MemTotal, cur.MemUsed = memInfo()
		cur.ProcRSS = procRSS()
		if rx, tx, err := netBytes(); err == nil {
			if rx >= prevRx && tx >= prevTx {
				cur.NetRxBps = (rx - prevRx) * 8 / uint64(every/time.Second)
				cur.NetTxBps = (tx - prevTx) * 8 / uint64(every/time.Second)
			}
			prevRx, prevTx = rx, tx
		}
		s.mu.Lock()
		s.cur = cur
		s.mu.Unlock()
	}
}

func cpuTimes() (idle, total uint64, err error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if idle, total, ok := parseCPULine(sc.Text()); ok {
			return idle, total, nil
		}
	}
	return 0, 0, sc.Err()
}

// parseCPULine reads the summary "cpu" line of /proc/stat: user nice system idle iowait
// irq softirq steal guest guest_nice. Idle, iowait and steal are not this server's work:
// steal is time the hypervisor gave its neighbours, and on a busy host it looked like load
// the server never had. guest and guest_nice are already inside user and nice, so they are
// not added again.
func parseCPULine(line string) (idle, total uint64, ok bool) {
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, 0, false
	}
	for i, v := range fields[1:min(len(fields), 9)] {
		n, _ := strconv.ParseUint(v, 10, 64)
		total += n
		if i == 3 || i == 4 || i == 7 { // idle, iowait, steal
			idle += n
		}
	}
	return idle, total, true
}

func memInfo() (total, used uint64) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	var avail uint64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		kb, _ := strconv.ParseUint(fields[1], 10, 64)
		switch fields[0] {
		case "MemTotal:":
			total = kb * 1024
		case "MemAvailable:":
			avail = kb * 1024
		}
	}
	if total > avail {
		used = total - avail
	}
	return total, used
}

// procCPUTime is this process's user and system time in clock ticks (/proc/self/stat).
func procCPUTime() (uint64, error) {
	raw, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0, err
	}
	return parseProcCPU(string(raw))
}

// parseProcCPU reads utime and stime from a /proc/<pid>/stat line. The command name in
// parentheses may hold spaces and parentheses, so the fields are counted after the last ")":
// state is field 3 there, utime 14 and stime 15.
func parseProcCPU(line string) (uint64, error) {
	i := strings.LastIndexByte(line, ')')
	if i < 0 {
		return 0, errors.New("proc stat: no command name")
	}
	fields := strings.Fields(line[i+1:])
	if len(fields) < 13 {
		return 0, errors.New("proc stat: short line")
	}
	utime, err := strconv.ParseUint(fields[11], 10, 64)
	if err != nil {
		return 0, err
	}
	stime, err := strconv.ParseUint(fields[12], 10, 64)
	if err != nil {
		return 0, err
	}
	return utime + stime, nil
}

func procRSS() uint64 {
	raw, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				kb, _ := strconv.ParseUint(fields[1], 10, 64)
				return kb * 1024
			}
		}
	}
	return 0
}

// netBytes sums all interfaces except loopback and container bridges.
func netBytes() (rx, tx uint64, err error) {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		if name == "lo" || strings.HasPrefix(name, "docker") || strings.HasPrefix(name, "veth") || strings.HasPrefix(name, "br-") {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) < 9 {
			continue
		}
		r, _ := strconv.ParseUint(fields[0], 10, 64)
		t, _ := strconv.ParseUint(fields[8], 10, 64)
		rx += r
		tx += t
	}
	return rx, tx, sc.Err()
}
