package node

import "testing"

// utime and stime are counted after the command name, which may hold spaces and parentheses.
func TestParseProcCPU(t *testing.T) {
	for name, tc := range map[string]struct {
		line string
		want uint64
		ok   bool
	}{
		"plain":  {"1662500 (mikan-node) S 1 1 1 0 -1 4194560 12 0 0 0 4100 2038 0 0 20 0 9 0 33 1317952000 17263", 6138, true},
		"spaces": {"7 (a (b) c) R 1 1 1 0 -1 0 0 0 0 0 10 5 0 0 20 0 1 0 1 1 1", 15, true},
		"short":  {"7 (x) R 1 2", 0, false},
		"broken": {"no name here", 0, false},
	} {
		got, err := parseProcCPU(tc.line)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("%s: got %d, %v", name, got, err)
		}
	}
}

// Steal (the hypervisor's neighbours) is not the server's load, and guest time is inside
// user already.
func TestParseCPULine(t *testing.T) {
	// user nice system idle iowait irq softirq steal guest guest_nice
	idle, total, ok := parseCPULine("cpu  10 0 10 60 5 0 5 10 7 0")
	if !ok || total != 100 || idle != 75 {
		t.Fatalf("idle %d, total %d, ok %v", idle, total, ok)
	}
	if _, total, ok := parseCPULine("cpu 1 2 3 4 5"); !ok || total != 15 {
		t.Fatalf("an old kernel's short line: total %d, ok %v", total, ok)
	}
	if _, _, ok := parseCPULine("cpu0 1 2 3 4 5"); ok {
		t.Fatal("a single core's line taken for the summary")
	}
}
