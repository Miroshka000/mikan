package addons

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mikan/internal/panel/store/db"
)

// A fake of the table the fetches are recorded in.
type recordedSources struct {
	mu    sync.Mutex
	keys  map[int64]string
	fetch map[int64]db.SetAddonSourceFetchParams
}

func newRecorded() *recordedSources {
	return &recordedSources{keys: map[int64]string{}, fetch: map[int64]db.SetAddonSourceFetchParams{}}
}

func (r *recordedSources) SetAddonSourceFetch(_ context.Context, a db.SetAddonSourceFetchParams) error {
	r.mu.Lock()
	r.fetch[a.ID] = a
	r.mu.Unlock()
	return nil
}

func (r *recordedSources) SetAddonSourceKey(_ context.Context, a db.SetAddonSourceKeyParams) error {
	r.mu.Lock()
	r.keys[a.ID] = a.Key
	r.mu.Unlock()
	return nil
}

// slowSource serves one file after a delay and counts the requests for it. status other than
// 200 is what it answers instead.
type slowSource struct {
	srv    *httptest.Server
	hits   atomic.Int32
	delay  atomic.Int64 // nanoseconds
	status atomic.Int32
	body   []byte
}

func newSlowSource(t *testing.T, body []byte) *slowSource {
	t.Helper()
	s := &slowSource{body: body}
	s.status.Store(http.StatusOK)
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mikan-addon.json" {
			http.NotFound(w, r)
			return
		}
		s.hits.Add(1)
		select {
		case <-time.After(time.Duration(s.delay.Load())):
		case <-r.Context().Done():
			return
		}
		if st := int(s.status.Load()); st != http.StatusOK {
			http.Error(w, "no", st)
			return
		}
		_, _ = w.Write(s.body)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *slowSource) row() db.AddonSource {
	return db.AddonSource{ID: 1, Url: s.srv.URL + "/mikan-addon.json"}
}

// settled waits until what the source's fetch left in memory satisfies ok.
func settled(t *testing.T, m *Manager, id int64, ok func(SourceState) bool) SourceState {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if st, have := m.Source(id); have && ok(st) {
			return st
		}
		time.Sleep(10 * time.Millisecond)
	}
	st, _ := m.Source(id)
	t.Fatalf("the source did not settle: %+v", st)
	return st
}

// Asking again for a source whose fetch is under way joins it: a page polled every few seconds
// does not pile up a fetch per poll on a slow source.
func TestRefreshSourceIsOneFetchAtATime(t *testing.T) {
	src := newSlowSource(t, mustJSON(t, entryDoc("hello", "b")))
	src.delay.Store(int64(300 * time.Millisecond))
	m := srcManager(t, devNet())
	rec := newRecorded()

	var wg sync.WaitGroup
	states := make([]SourceState, 8)
	for i := range states {
		wg.Add(1)
		go func() {
			defer wg.Done()
			states[i] = m.RefreshSource(t.Context(), rec, src.row(), false, "")
		}()
	}
	wg.Wait()
	if n := src.hits.Load(); n != 1 {
		t.Fatalf("%d fetches for 8 asks at once", n)
	}
	for i, st := range states {
		if st.Err != nil || len(st.Doc.Entries) != 1 {
			t.Errorf("ask %d: %+v", i, st)
		}
	}
	// Fresh now: no fetch at all.
	if st := m.RefreshSource(t.Context(), rec, src.row(), false, ""); len(st.Doc.Entries) != 1 || src.hits.Load() != 1 {
		t.Fatalf("a fresh source fetched again: %d", src.hits.Load())
	}
}

// A request that goes away (a tab closed) leaves the fetch to finish: the source is not
// recorded as failed, and its entries are not wiped from the catalog.
func TestRefreshSourceSurvivesACancelledRequest(t *testing.T) {
	src := newSlowSource(t, mustJSON(t, entryDoc("hello", "b")))
	src.delay.Store(int64(300 * time.Millisecond))
	m := srcManager(t, devNet())
	rec := newRecorded()

	ctx, cancel := context.WithCancel(t.Context())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	m.RefreshSource(ctx, rec, src.row(), true, "")
	st := settled(t, m, 1, func(SourceState) bool { return true })
	if st.Err != nil && errors.Is(st.Err, context.Canceled) {
		t.Fatalf("the cancelled request is the source's fault: %+v", st)
	}
	st = settled(t, m, 1, func(s SourceState) bool { return s.Err == nil && len(s.Doc.Entries) == 1 })
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.fetch[1].Error != "" {
		t.Fatalf("recorded as failed: %+v", rec.fetch[1])
	}
}

// A page is not made to wait for a source it already knows: the old state comes at once and the
// fetch brings the new one. An install (force) waits for the answer.
func TestRefreshSourceServesTheOldStateWhileFetching(t *testing.T) {
	src := newSlowSource(t, mustJSON(t, entryDoc("hello", "b")))
	m := srcManager(t, devNet())
	var offset atomic.Int64
	m.now = func() time.Time { return time.Now().Add(time.Duration(offset.Load())) }
	rec := newRecorded()
	if st := m.RefreshSource(t.Context(), rec, src.row(), false, ""); st.Err != nil {
		t.Fatal(st.Err)
	}

	offset.Store(int64(sourceFresh + time.Minute)) // stale now
	src.delay.Store(int64(time.Second))
	start := time.Now()
	st := m.RefreshSource(t.Context(), rec, src.row(), false, "")
	if time.Since(start) > 500*time.Millisecond || len(st.Doc.Entries) != 1 {
		t.Fatalf("a stale source made the page wait: %v %+v", time.Since(start), st)
	}
	settled(t, m, 1, func(s SourceState) bool { return s.At.After(st.At) })
	if src.hits.Load() != 2 {
		t.Fatalf("%d fetches", src.hits.Load())
	}

	// A source not known yet is waited for only a few seconds, and keeps being fetched.
	m2 := srcManager(t, devNet())
	src.delay.Store(int64(sourceWait + 500*time.Millisecond))
	start = time.Now()
	st = m2.RefreshSource(t.Context(), rec, src.row(), false, "")
	if d := time.Since(start); d > sourceWait+400*time.Millisecond || !st.At.IsZero() {
		t.Fatalf("a source not known yet: %v %+v", d, st)
	}
	settled(t, m2, 1, func(s SourceState) bool { return s.Err == nil && len(s.Doc.Entries) == 1 })
}

// A source that cannot be reached for now keeps what it offered; one that is refused for what
// it is (the key, a signature) offers nothing.
func TestRefreshSourceKeepsTheLastGoodEntriesWhenUnreachable(t *testing.T) {
	src := newSlowSource(t, mustJSON(t, entryDoc("hello", "b")))
	m := srcManager(t, devNet())
	rec := newRecorded()
	if st := m.RefreshSource(t.Context(), rec, src.row(), true, ""); st.Err != nil || !st.Offered() {
		t.Fatalf("first: %+v", st)
	}
	for _, code := range []int{http.StatusInternalServerError, http.StatusForbidden, http.StatusTooManyRequests} {
		src.status.Store(int32(code))
		st := m.RefreshSource(t.Context(), rec, src.row(), true, "")
		if SourceCode(st.Err) != "source_unreachable" || !st.Offered() || len(st.Doc.Entries) != 1 {
			t.Fatalf("HTTP %d: %+v", code, st)
		}
	}
	rec.mu.Lock()
	if rec.fetch[1].Error != "source_unreachable" {
		t.Errorf("the table says %+v", rec.fetch[1])
	}
	rec.mu.Unlock()
	// A source never read has nothing to keep.
	src.status.Store(http.StatusInternalServerError)
	if st := srcManager(t, devNet()).RefreshSource(t.Context(), rec, src.row(), true, ""); st.Offered() {
		t.Fatalf("offered with nothing read: %+v", st)
	}
	// What is not the network's fault clears the entries: the file is not signed with the key
	// the source is pinned to.
	src.status.Store(http.StatusOK)
	row := src.row()
	row.Key = "AAAA"
	st := m.RefreshSource(t.Context(), rec, row, true, "")
	if !errors.Is(st.Err, ErrSourceKeyChanged) || st.Offered() || len(st.Doc.Entries) != 0 {
		t.Fatalf("key changed: %+v", st)
	}
}

// A source that keeps failing is asked for less and less often; GitHub's shorthand is read
// rarely to begin with.
func TestSourceTTL(t *testing.T) {
	ok := SourceState{}
	if d := sourceTTL("https://example.com/a.json", ok); d != sourceFresh {
		t.Errorf("fresh: %v", d)
	}
	if d := sourceTTL("github.com/o/r", ok); d != githubFresh {
		t.Errorf("github fresh: %v", d)
	}
	failed := func(n int) SourceState { return SourceState{Err: ErrSourceUnreachable, Fails: n} }
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, sourceFresh, sourceFresh}
	for i, w := range want {
		if d := sourceTTL("https://example.com/a.json", failed(i+1)); d != w {
			t.Errorf("failure %d: %v, want %v", i+1, d, w)
		}
	}
	if d := sourceTTL("github.com/o/r", failed(6)); d != 30*time.Minute {
		t.Errorf("github backed off: %v", d)
	}
	if d := sourceTTL("https://example.com/a.json", failed(1000)); d != sourceFresh {
		t.Errorf("a long run of failures: %v", d)
	}
}

// Object stores answer 403 for a key that is not there, and some hosts answer every path with
// their page: neither is a signature. A pinned source is held to its key all the same.
func TestSourceSignatureAbsentOnCommonHosts(t *testing.T) {
	data := mustJSON(t, entryDoc("hello", "b"))
	var sigStatus atomic.Int32
	var sigBody atomic.Value
	sigBody.Store("")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/mikan-addon.json":
			_, _ = w.Write(data)
		case "/mikan-addon.json.sig":
			if st := int(sigStatus.Load()); st != http.StatusOK {
				http.Error(w, "no", st)
				return
			}
			_, _ = w.Write([]byte(sigBody.Load().(string)))
		}
	}))
	t.Cleanup(srv.Close)
	m := srcManager(t, devNet())
	url := srv.URL + "/mikan-addon.json"
	pinned := SourcePin{Key: "A" + strings.Repeat("A", 42) + "="}
	for _, status := range []int32{http.StatusNotFound, http.StatusForbidden, http.StatusGone} {
		sigStatus.Store(status)
		if doc, err := m.FetchSource(t.Context(), url, SourcePin{}); err != nil || doc.Key != "" || len(doc.Entries) != 1 {
			t.Errorf("HTTP %d for the signature: %+v %v", status, doc, err)
		}
		if _, err := m.FetchSource(t.Context(), url, pinned); !errors.Is(err, ErrSourceKeyChanged) {
			t.Errorf("HTTP %d for the signature of a pinned source: %v", status, err)
		}
	}
	// A server error on the signature is still an error.
	sigStatus.Store(http.StatusInternalServerError)
	if _, err := m.FetchSource(t.Context(), url, SourcePin{}); !errors.Is(err, ErrSourceUnreachable) {
		t.Errorf("HTTP 500 for the signature: %v", err)
	}
	// A page in place of the signature.
	sigStatus.Store(http.StatusOK)
	sigBody.Store("<!doctype html><title>app</title>")
	if doc, err := m.FetchSource(t.Context(), url, SourcePin{}); err != nil || doc.Key != "" {
		t.Errorf("a page for the signature: %+v %v", doc, err)
	}
	if _, err := m.FetchSource(t.Context(), url, pinned); !errors.Is(err, ErrSourceSignature) {
		t.Errorf("a page for the signature of a pinned source: %v", err)
	}
}

// Which languages of a source's text survive does not depend on the order a map happens to give.
func TestCleanTextKeepsTheSameLanguages(t *testing.T) {
	in := Text{"en": "english", "ru": "русский"}
	for _, l := range []string{"de", "fr", "es", "it", "pt", "nl", "pl", "tr", "ja", "ko", "zh"} {
		in[l] = l
	}
	first := cleanText(in, 80)
	if len(first) != 8 || first["en"] == "" || first["ru"] == "" {
		t.Fatalf("kept: %v", first)
	}
	for range 50 {
		got := cleanText(in, 80)
		for l := range first {
			if got[l] != first[l] {
				t.Fatalf("the languages changed: %v then %v", first, got)
			}
		}
		if len(got) != len(first) {
			t.Fatalf("the languages changed: %v then %v", first, got)
		}
	}
}
