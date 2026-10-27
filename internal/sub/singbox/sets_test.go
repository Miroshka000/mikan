package singbox

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"
)

// A Clash rule list is YAML with a payload or a line per entry, comments and blank lines
// left out; a YAML of no payload is read as lines.
func TestParseList(t *testing.T) {
	for _, c := range []struct {
		name string
		raw  string
		want []string
	}{
		{"payload", "payload:\n  - DOMAIN-SUFFIX,example.com\n  - '+.example.org'\n", []string{"DOMAIN-SUFFIX,example.com", "+.example.org"}},
		{"lines", "# a list\n\nexample.com\r\n  # indented\n  10.0.0.0/8  \n\n", []string{"example.com", "10.0.0.0/8"}},
		{"empty", "", nil},
		{"empty payload", "payload: []\n", []string{"payload: []"}},
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
		got, err := cachedList(t.Context(), src)
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

// A list never fetched is an error, tried again five minutes later.
func TestCachedListFailure(t *testing.T) {
	l := &listServer{status: http.StatusNotFound}
	src, age := cachedSrc(t, l)

	for range 2 {
		if got, err := cachedList(t.Context(), src); err == nil || l.count() != 1 {
			t.Fatalf("got %q %v after %d fetches, want an error after 1", got, err, l.count())
		}
	}
	l.set(http.StatusOK, "payload:\n  - ya.ru\n")
	age(5 * time.Minute)
	if got, err := cachedList(t.Context(), src); err != nil || !slices.Equal(got, []string{"ya.ru"}) || l.count() != 2 {
		t.Fatalf("got %q %v after %d fetches, want [ya.ru] after 2", got, err, l.count())
	}
}
