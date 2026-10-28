package singbox

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"mikan/internal/sub/routing"
)

// A Clash rule list is YAML with a payload or a line per entry, comments and blank lines
// left out; a YAML of no payload is read as lines, and an empty payload is an empty list.
func TestParseList(t *testing.T) {
	for _, c := range []struct {
		name string
		raw  string
		want []string
	}{
		{"payload", "payload:\n  - DOMAIN-SUFFIX,example.com\n  - '+.example.org'\n", []string{"DOMAIN-SUFFIX,example.com", "+.example.org"}},
		{"lines", "# a list\n\nexample.com\r\n  # indented\n  10.0.0.0/8  \n\n", []string{"example.com", "10.0.0.0/8"}},
		{"empty", "", nil},
		{"empty payload", "payload: []\n", nil},
		{"null payload", "# nothing yet\npayload:\n", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := parseList([]byte(c.raw)); !slices.Equal(got, c.want) {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

// A list is fetched and read; a status other than 200 is an error, not an empty list.
func TestFetchList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ru.yaml" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("payload:\n  - +.gosuslugi.ru\n  - ya.ru\n"))
	}))
	defer srv.Close()

	got, err := fetchList(t.Context(), srv.URL+"/ru.yaml")
	if err != nil || !slices.Equal(got, []string{"+.gosuslugi.ru", "ya.ru"}) {
		t.Fatalf("%q %v", got, err)
	}
	if got, err := fetchList(t.Context(), srv.URL+"/gone.yaml"); err == nil {
		t.Fatalf("404 read as a list: %q", got)
	}
}

// listServer serves one Clash list that a test changes between fetches, and counts them.
type listServer struct {
	sync.Mutex
	status  int
	body    string
	fetches int
}

func (l *listServer) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	l.Lock()
	defer l.Unlock()
	l.fetches++
	w.WriteHeader(l.status)
	_, _ = w.Write([]byte(l.body))
}

func (l *listServer) set(status int, body string) {
	l.Lock()
	l.status, l.body = status, body
	l.Unlock()
}

func (l *listServer) count() int {
	l.Lock()
	defer l.Unlock()
	return l.fetches
}

// cachedSrc is the address of a new server of l, out of the cache when the test ends,
// and age moves its cache entry d back in time.
func cachedSrc(t *testing.T, l *listServer) (src string, age func(d time.Duration)) {
	srv := httptest.NewServer(l)
	t.Cleanup(srv.Close)
	src = srv.URL + "/ru.yaml"
	t.Cleanup(func() {
		listCache.Lock()
		delete(listCache.m, src)
		listCache.Unlock()
	})
	return src, func(d time.Duration) {
		listCache.Lock()
		e := listCache.m[src]
		e.at = e.at.Add(-d)
		listCache.m[src] = e
		listCache.Unlock()
	}
}

// A fetched list lives a day, so a list that follows a branch is fetched again; a failed
// fetch is tried again five minutes later, and until one succeeds the last payload stays.
func TestCachedList(t *testing.T) {
	l := &listServer{status: http.StatusOK, body: "payload:\n  - ya.ru\n"}
	src, age := cachedSrc(t, l)
	get := func(want string, fetches int) {
		t.Helper()
		got, err := cachedList(t.Context(), src, listTTL)
		if err != nil || !slices.Equal(got, []string{want}) || l.count() != fetches {
			t.Fatalf("got %q %v after %d fetches, want %q after %d", got, err, l.count(), want, fetches)
		}
	}

	get("ya.ru", 1)
	l.set(http.StatusOK, "payload:\n  - vk.com\n")
	age(time.Hour)
	get("ya.ru", 1) // an hour old: from the cache
	age(24 * time.Hour)
	get("vk.com", 2) // a day old: fetched again

	l.set(http.StatusBadGateway, "")
	age(25 * time.Hour)
	get("vk.com", 3) // the fetch failed: the last payload stays
	age(4 * time.Minute)
	get("vk.com", 3) // the failure is not tried again at once
	age(time.Minute)
	get("vk.com", 4) // five minutes later it is, and fails again

	l.set(http.StatusOK, "payload:\n  - ok.ru\n")
	age(5 * time.Minute)
	get("ok.ru", 5)
	age(23 * time.Hour)
	get("ok.ru", 5) // a success lives a day again
}

// A fetch that fails does not undo one that ended while it ran: the new list stays.
func TestCachedListFailureAfterFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		listCache.Lock()
		listCache.m["http://"+r.Host+r.URL.Path] = listEntry{payload: []string{"vk.com"}, at: time.Now()}
		listCache.Unlock()
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	src := srv.URL + "/ru.yaml"
	listCache.Lock()
	listCache.m[src] = listEntry{payload: []string{"ya.ru"}, at: time.Now().Add(-25 * time.Hour)}
	listCache.Unlock()
	t.Cleanup(func() {
		listCache.Lock()
		delete(listCache.m, src)
		listCache.Unlock()
	})

	got, err := cachedList(t.Context(), src, listTTL)
	listCache.Lock()
	e := listCache.m[src]
	listCache.Unlock()
	if err != nil || !slices.Equal(got, []string{"vk.com"}) || e.err != nil || !slices.Equal(e.payload, []string{"vk.com"}) {
		t.Fatalf("got %q %v, cached %q %v; want vk.com, no error", got, err, e.payload, e.err)
	}
}

// A list never fetched is an error, tried again five minutes later.
func TestCachedListFailure(t *testing.T) {
	l := &listServer{status: http.StatusNotFound}
	src, age := cachedSrc(t, l)

	for range 2 {
		if got, err := cachedList(t.Context(), src, listTTL); err == nil || l.count() != 1 {
			t.Fatalf("got %q %v after %d fetches, want an error after 1", got, err, l.count())
		}
	}
	l.set(http.StatusOK, "payload:\n  - ya.ru\n")
	age(5 * time.Minute)
	if got, err := cachedList(t.Context(), src, listTTL); err != nil || !slices.Equal(got, []string{"ya.ru"}) || l.count() != 2 {
		t.Fatalf("got %q %v after %d fetches, want [ya.ru] after 2", got, err, l.count())
	}
}

// A list lives as long as its rule-provider's interval, within an hour and a week, and a
// day when it gives none; of providers of one address the shortest wins, and the others'
// addresses do not count.
func TestListTTLOf(t *testing.T) {
	const src, other = "https://example.com/ru.yaml", "https://example.com/other.yaml"
	for _, c := range []struct {
		name      string
		intervals []int // of the providers at src
		want      time.Duration
	}{
		{"none", nil, listTTL},
		{"unset", []int{0}, listTTL},
		{"interval", []int{6 * 3600}, 6 * time.Hour},
		{"below an hour", []int{60}, time.Hour},
		{"above a week", []int{30 * 86400}, 7 * 24 * time.Hour},
		{"negative", []int{-1}, listTTL},
		{"shared, one unset", []int{0, 2 * 86400}, listTTL},
		{"shared", []int{12 * 3600, 2 * 3600}, 2 * time.Hour},
		{"shared, clamped", []int{60, 3 * 3600}, time.Hour},
	} {
		t.Run(c.name, func(t *testing.T) {
			lists := map[string]routing.Provider{"other": {Name: "other", URL: other, Interval: 60}}
			for i, n := range c.intervals {
				name := fmt.Sprint("ru", i)
				lists[name] = routing.Provider{Name: name, URL: src, Interval: n}
			}
			if got := listTTLOf(lists, src); got != c.want {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

// The renderer asks for a list with the TTL of every provider of its address.
func TestListAsksTTL(t *testing.T) {
	var got []time.Duration
	old := singboxList
	singboxList = func(_ context.Context, _ string, ttl time.Duration) ([]string, error) {
		got = append(got, ttl)
		return []string{"ya.ru"}, nil
	}
	t.Cleanup(func() { singboxList = old })
	const src = "https://example.com/ru.yaml"
	s := &ruleSets{ctx: t.Context(), used: map[string]map[string]any{}, lists: map[string]routing.Provider{
		"ru":   {Name: "ru", Behavior: "domain", URL: src, Interval: 3 * 3600},
		"ru-2": {Name: "ru-2", Behavior: "domain", URL: src, Interval: 86400},
		"day":  {Name: "day", Behavior: "domain", URL: "https://example.com/day.yaml"},
	}}
	for _, name := range []string{"ru-2", "day"} {
		if _, ok := s.list(name); !ok {
			t.Fatalf("%s: no rule set", name)
		}
	}
	if want := []time.Duration{3 * time.Hour, listTTL}; !slices.Equal(got, want) {
		t.Fatalf("TTLs %v, want %v", got, want)
	}
}

// A list fetched with the TTL of its interval is fetched again when that runs out, not a
// day later, and one of a week lives past a day; a failure is still tried again five
// minutes later.
func TestCachedListTTL(t *testing.T) {
	l := &listServer{status: http.StatusOK, body: "payload:\n  - ya.ru\n"}
	src, age := cachedSrc(t, l)
	get := func(ttl time.Duration, want string, fetches int) {
		t.Helper()
		got, err := cachedList(t.Context(), src, ttl)
		if err != nil || !slices.Equal(got, []string{want}) || l.count() != fetches {
			t.Fatalf("got %q %v after %d fetches, want %q after %d", got, err, l.count(), want, fetches)
		}
	}

	get(2*time.Hour, "ya.ru", 1)
	l.set(http.StatusOK, "payload:\n  - vk.com\n")
	age(time.Hour + 59*time.Minute)
	get(2*time.Hour, "ya.ru", 1) // within its two hours: from the cache
	age(time.Minute)
	get(2*time.Hour, "vk.com", 2) // two hours old: fetched again

	week := 7 * 24 * time.Hour
	l.set(http.StatusOK, "payload:\n  - ok.ru\n")
	age(6 * 24 * time.Hour)
	get(week, "vk.com", 2) // six days old: a list of a week stays
	age(24 * time.Hour)
	get(week, "ok.ru", 3)

	l.set(http.StatusBadGateway, "")
	age(week)
	get(week, "ok.ru", 4) // the fetch failed: the last payload stays
	age(5 * time.Minute)
	get(week, "ok.ru", 5) // and is tried again five minutes later, not a week
}
