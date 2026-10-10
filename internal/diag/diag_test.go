package diag

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mikan/internal/nodeapi"
)

func byID(d nodeapi.Diagnosis) map[string]nodeapi.DiagItem {
	m := map[string]nodeapi.DiagItem{}
	for _, it := range d.Items {
		m[it.ID] = it
	}
	return m
}

// Everything reachable: local stand-ins for the internet, GitHub and GHCR. GHCR answers 401
// as the real one does, which is an answer all the same. The stand-ins' clock is two
// minutes ahead: the server's clock is behind.
func TestRunAllReachable(t *testing.T) {
	ahead := time.Now().Add(2 * time.Minute)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Date", ahead.UTC().Format(http.TimeFormat))
		if strings.HasPrefix(r.URL.Path, "/v2/") {
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer srv.Close()
	d := Run(context.Background(), Options{
		Targets: Targets{Name: "localhost", Internet: []string{srv.Listener.Addr().String()}, GitHub: srv.URL + "/", GHCR: srv.URL + "/v2/"},
		HTTP:    srv.Client(), DataDir: "/data",
		Disk:   func(string) (uint64, uint64, bool) { return 1 << 30, 10 << 30, true },
		Memory: func() (uint64, uint64, bool) { return 50 << 20, 1 << 30, true },
	})
	got := byID(d)
	for _, id := range []string{"dns", "internet", "github", "ghcr"} {
		if got[id].Status != nodeapi.CheckOK {
			t.Errorf("%s: %+v", id, got[id])
		}
	}
	if got["ghcr"].Params["status"] != "401" {
		t.Errorf("ghcr: %+v", got["ghcr"])
	}
	if c := got["clock"]; c.Status != nodeapi.CheckWarn || c.Code != "skew" || !strings.HasPrefix(c.Params["skew"], "-1") {
		t.Errorf("clock two minutes behind: %+v", c)
	}
	if c := got["disk"]; c.Status != nodeapi.CheckWarn || c.Code != "low" {
		t.Errorf("1 GiB free: %+v", c)
	}
	if c := got["memory"]; c.Status != nodeapi.CheckWarn {
		t.Errorf("50 MiB available: %+v", c)
	}
	if len(d.Items) != 7 {
		t.Errorf("%d items", len(d.Items))
	}
}

// Nothing reachable: a closed port everywhere, a name that does not exist.
func TestRunNothingReachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := ln.Addr().String()
	ln.Close()
	d := Run(context.Background(), Options{
		Targets: Targets{Name: "github.com", Internet: []string{closed}, GitHub: "https://" + closed + "/", GHCR: "https://" + closed + "/v2/"},
		// A resolver whose server does not answer.
		Resolver: &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", closed)
		}},
		Disk:    func(string) (uint64, uint64, bool) { return 100 << 20, 10 << 30, true },
		Memory:  func() (uint64, uint64, bool) { return 0, 0, false },
		DataDir: "/data",
	})
	got := byID(d)
	if got["dns"].Status != nodeapi.CheckFail {
		t.Errorf("dns: %+v", got["dns"])
	}
	for _, id := range []string{"internet", "github", "ghcr"} {
		if got[id].Status != nodeapi.CheckFail || got[id].Code != nodeapi.LinkRefused {
			t.Errorf("%s: %+v", id, got[id])
		}
	}
	if got["clock"].Status != nodeapi.CheckSkip || got["memory"].Status != nodeapi.CheckSkip {
		t.Errorf("unknowns: %+v %+v", got["clock"], got["memory"])
	}
	if got["disk"].Status != nodeapi.CheckFail {
		t.Errorf("100 MiB free: %+v", got["disk"])
	}
}

func TestClock(t *testing.T) {
	for _, c := range []struct {
		skew time.Duration
		want string
	}{{0, nodeapi.CheckOK}, {29 * time.Second, nodeapi.CheckOK}, {30 * time.Second, nodeapi.CheckWarn}, {-45 * time.Second, nodeapi.CheckWarn}} {
		if got := Clock(&c.skew); got.Status != c.want {
			t.Errorf("%v: %+v", c.skew, got)
		}
	}
	if got := Clock(nil); got.Status != nodeapi.CheckSkip {
		t.Errorf("unknown: %+v", got)
	}
}

func TestParseMeminfo(t *testing.T) {
	a, tot, ok := ParseMeminfo(strings.NewReader("MemTotal:        2000000 kB\nMemFree:  10 kB\nMemAvailable:     1000 kB\n"))
	if !ok || a != 1000<<10 || tot != 2000000<<10 {
		t.Fatalf("%d %d %v", a, tot, ok)
	}
	if _, _, ok := ParseMeminfo(strings.NewReader("MemTotal: 1 kB\n")); ok {
		t.Fatal("no MemAvailable (kernels before 3.14): not known")
	}
}
