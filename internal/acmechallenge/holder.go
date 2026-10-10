package acmechallenge

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Holder names the program listening on TCP port, as the kernel's tables tell: "nginx",
// "caddy", "apache2"… "" when it cannot be told: not Linux, or the program runs as
// another user (a node's container reads only its own processes' descriptors).
func Holder(port int) string { return holderIn("/proc", port) }

func holderIn(proc string, port int) string {
	inodes := map[string]bool{}
	for _, f := range []string{"net/tcp", "net/tcp6"} {
		raw, err := os.ReadFile(filepath.Join(proc, f))
		if err != nil {
			continue
		}
		for line := range strings.Lines(string(raw)) {
			fs := strings.Fields(line)
			// sl local rem st tx:rx tr:when retrnsmt uid timeout inode
			if len(fs) < 10 || fs[3] != "0A" {
				continue
			}
			_, hex, ok := strings.Cut(fs[1], ":")
			if !ok {
				continue
			}
			if p, err := strconv.ParseUint(hex, 16, 16); err == nil && int(p) == port && fs[9] != "0" {
				inodes["socket:["+fs[9]+"]"] = true
			}
		}
	}
	if len(inodes) == 0 {
		return ""
	}
	pids, err := os.ReadDir(proc)
	if err != nil {
		return ""
	}
	for _, p := range pids {
		if _, err := strconv.Atoi(p.Name()); err != nil {
			continue
		}
		fds, err := os.ReadDir(filepath.Join(proc, p.Name(), "fd"))
		if err != nil {
			continue
		}
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(proc, p.Name(), "fd", fd.Name()))
			if err == nil && inodes[link] {
				comm, err := os.ReadFile(filepath.Join(proc, p.Name(), "comm"))
				if err != nil {
					return ""
				}
				return strings.TrimSpace(string(comm))
			}
		}
	}
	return ""
}
