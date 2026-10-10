package checkhost

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fake is check-host.net as its API page describes it: two Russian checkers and one
// elsewhere; port 443 opens from Moscow and times out in Saint Petersburg, port 31234 is
// refused everywhere. The first poll finds nothing done yet.
type fake struct {
	mu      sync.Mutex
	started map[string]string // request id → host:port
	polls   map[string]int
	lists   atomic.Int32
	checks  atomic.Int32
	nodes   []string // the node= params of the last check
	accept  []string
}

func (f *fake) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /nodes/hosts", func(w http.ResponseWriter, r *http.Request) {
		f.lists.Add(1)
		_, _ = w.Write([]byte(`{"nodes":{
			"ru2.node.check-host.net":{"asn":"AS1","ip":"192.0.2.2","location":["ru","Russia","Saint Petersburg"]},
			"ru1.node.check-host.net":{"asn":"AS1","ip":"192.0.2.1","location":["ru","Russia","Moscow"]},
			"de1.node.check-host.net":{"asn":"AS2","ip":"192.0.2.3","location":["de","Germany","Frankfurt"]},
			"ru9 bad/../x":{"location":["ru","Russia","Nowhere"]}}}`))
	})
	mux.HandleFunc("GET /check-tcp", func(w http.ResponseWriter, r *http.Request) {
		f.checks.Add(1)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.accept = append(f.accept, r.Header.Get("Accept"))
		f.nodes = r.URL.Query()["node"]
		id := "req" + string(rune('a'+len(f.started)))
		f.started[id] = r.URL.Query().Get("host")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": 1, "request_id": id, "permanent_link": "https://check-host.net/check-report/" + id,
			"nodes": map[string][]string{"ru1.node.check-host.net": {"ru", "Russia", "Moscow", "192.0.2.1", "AS1"}}})
	})
	mux.HandleFunc("GET /check-result/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		id := r.PathValue("id")
		target, ok := f.started[id]
		f.polls[id]++
		first := f.polls[id] == 1
		f.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		if first {
			_, _ = w.Write([]byte(`{"ru1.node.check-host.net":null,"ru2.node.check-host.net":null}`))
			return
		}
		if strings.HasSuffix(target, ":443") {
			_, _ = w.Write([]byte(`{"ru1.node.check-host.net":[{"time":0.042,"address":"203.0.113.5"}],"ru2.node.check-host.net":[{"error":"Connection timed out"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"ru1.node.check-host.net":[{"error":"Connection refused"}],"ru2.node.check-host.net":[{"error":"Connection refused"}]}`))
	})
	return mux
}

func TestCheckTCP(t *testing.T) {
	f := &fake{started: map[string]string{}, polls: map[string]int{}}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	now := time.Unix(1_800_000_000, 0)
	c := New(srv.URL, srv.Client(), func() time.Time { return now })
	c.pollEvery = 10 * time.Millisecond
	res, err := c.CheckTCP(context.Background(), "203.0.113.5", []int{443, 31234})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Cities) != 2 || res.Cities[0].City != "Moscow" || res.Cities[1].City != "Saint Petersburg" {
		t.Fatalf("only the Russian checkers, in order: %+v", res.Cities)
	}
	moscow, spb := res.Cities[0].Ports, res.Cities[1].Ports
	if !moscow[0].OK || moscow[0].Ms != 42 || moscow[0].Port != 443 {
		t.Errorf("443 from Moscow: %+v", moscow[0])
	}
	if spb[0].OK || spb[0].Error != "Connection timed out" {
		t.Errorf("443 from Saint Petersburg: %+v", spb[0])
	}
	if moscow[1].OK || moscow[1].Error != "Connection refused" || moscow[1].Port != 31234 {
		t.Errorf("31234: %+v", moscow[1])
	}
	if len(f.nodes) != 2 || f.nodes[0] != "ru1.node.check-host.net" {
		t.Errorf("checkers asked: %v", f.nodes)
	}
	for _, a := range f.accept {
		if a != "application/json" {
			t.Errorf("Accept: %q", a)
		}
	}
	// The same check soon after is the kept answer: the service is not asked again.
	again, err := c.CheckTCP(context.Background(), "203.0.113.5", []int{443, 31234})
	if err != nil || !again.Cached || f.checks.Load() != 2 {
		t.Fatalf("again: cached %v, %d checks, %v", again.Cached, f.checks.Load(), err)
	}
	// Later the list of checkers is kept, the check is new.
	now = now.Add(3 * time.Minute)
	if r, err := c.CheckTCP(context.Background(), "203.0.113.5", []int{443, 31234}); err != nil || r.Cached || f.lists.Load() != 1 {
		t.Fatalf("later: cached %v, %d lists, %v", r.Cached, f.lists.Load(), err)
	}
}

func TestCheckTCPOneAtATime(t *testing.T) {
	c := New("http://127.0.0.1:1", nil, nil)
	c.run.Lock()
	defer c.run.Unlock()
	if _, err := c.CheckTCP(context.Background(), "203.0.113.5", []int{443}); !errors.Is(err, ErrBusy) {
		t.Fatalf("a second check: %v", err)
	}
}

func TestNoRussianCheckers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"nodes":{"de1.node.check-host.net":{"location":["de","Germany","Frankfurt"]}}}`))
	}))
	defer srv.Close()
	if _, err := New(srv.URL, srv.Client(), nil).CheckTCP(context.Background(), "203.0.113.5", []int{443}); !errors.Is(err, ErrNoCheckers) {
		t.Fatalf("%v", err)
	}
	down := New("http://127.0.0.1:1", nil, nil)
	if _, err := down.CheckTCP(context.Background(), "203.0.113.5", []int{443}); !errors.Is(err, ErrNoCheckers) {
		t.Fatalf("service down: %v", err)
	}
}
