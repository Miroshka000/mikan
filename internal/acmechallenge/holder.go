package acmechallenge

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Holder names the program listening on TCP port: as the kernel's tables tell ("nginx",
// "caddy", "apache2"…) when this process may read them, else by the Server header it
// answers with on the loopback ("nginx", "caddy", "apache"): a node's container reads only
// its own processes' descriptors, and a web server of the admin's runs as another user.
// "" when neither tells.
func Holder(port int) string {
	if h := holderIn("/proc", port); h != "" {
		return h
	}
	return serverOn("http://127.0.0.1:" + strconv.Itoa(port) + "/")
}

// serverOn asks url once and names the program by its Server header.
func serverOn(url string) string {
	c := http.Client{
		Timeout:       2 * time.Second,
		Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := c.Get(url)
	if err != nil {
		return ""
	}
	_ = resp.Body.Close()
	return serverName(resp.Header.Get("Server"))
}

// serverName is the product of a Server header in lower case: "nginx/1.24.0 (Ubuntu)" is
// "nginx", "Caddy" is "caddy". "" for a header that names nothing readable.
func serverName(h string) string {
	name, _, _ := strings.Cut(strings.TrimSpace(h), "/")
	name, _, _ = strings.Cut(name, " ")
	name = strings.ToLower(name)
	if name == "" || len(name) > 32 {
		return ""
	}
	for _, c := range []byte(name) {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return ""
		}
	}
	return name
}

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
