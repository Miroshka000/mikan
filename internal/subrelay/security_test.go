package subrelay

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestOversizedRequestNeverReachesPanel(t *testing.T) {
	p, _ := New("https://panel.example/api/subscription", "key", "sub")
	p.SetCircuitBreaker(0, 0)
	calls := 0
	p.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: http.NoBody}, nil
	})}
	r := httptest.NewRequest("POST", "https://sub.example/sub"+tokenPath+"/devices/1/name", strings.NewReader(strings.Repeat("x", (1<<20)+1)))
	r.ContentLength = -1 // also enforce the limit for chunked requests
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != 413 || calls != 0 {
		t.Fatalf("code=%d upstream calls=%d", w.Code, calls)
	}
}

func TestInfoRefusalInvalidatesProfile(t *testing.T) {
	p, _ := New("https://panel.example/api/subscription", "key", "sub")
	p.SetCircuitBreaker(0, 0)
	p.remember("profile", tokenPath, entry{body: []byte("profile"), header: http.Header{"Content-Type": []string{"text/plain"}}}, p.generation())
	p.forget(tokenPath+"/info", false)
	if len(p.profiles) != 0 {
		t.Fatal("info refusal left an offline profile accessible")
	}
}

func FuzzProxyCannotEscapeUpstream(f *testing.F) {
	for _, path := range []string{tokenPath, "/assets/app.js", "/../admin", "//attacker.test/a", "/assets/../../admin", "/assets/%2e%2e/admin", "/assets/\\admin", "/tg", "/brand/image.png"} {
		f.Add(path)
	}
	f.Fuzz(func(t *testing.T, path string) {
		p, _ := New("https://panel.example/api/subscription", "edge-key", "sub")
		p.SetCircuitBreaker(0, 0)
		p.client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Scheme != "https" || r.URL.Host != "panel.example" || !strings.HasPrefix(r.URL.Path, "/api/subscription/") {
				t.Fatalf("proxy escaped upstream: %s", r.URL)
			}
			if r.Header.Get("Authorization") != "Bearer edge-key" || r.Header.Get("Cookie") != "" {
				t.Fatal("credential isolation failed")
			}
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("ok"))}, nil
		})}
		r := httptest.NewRequest("GET", "https://sub.example/", nil)
		r.URL.Path = "/sub" + path
		r.Header.Set("Authorization", "Bearer client-secret")
		r.Header.Set("Cookie", "admin=secret")
		p.ServeHTTP(httptest.NewRecorder(), r)
	})
}

func TestConcurrentRequestLimit(t *testing.T) {
	p, _ := New("https://panel.example/api/subscription", "key", "sub")
	p.SetCircuitBreaker(0, 0)
	started := make(chan struct{}, 32)
	release := make(chan struct{})
	p.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		started <- struct{}{}
		<-release
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("ok"))}, nil
	})}
	fetch := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		p.ServeHTTP(w, httptest.NewRequest("GET", "https://sub.example/sub"+tokenPath, nil))
		return w
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			if w := fetch(); w.Code != 200 {
				t.Errorf("active request: %d", w.Code)
			}
		})
	}
	for range 16 {
		<-started
	}
	w := fetch()
	if w.Code != 503 || w.Header().Get("Retry-After") != "1" {
		t.Errorf("overload not rejected: %d", w.Code)
	}
	close(release)
	wg.Wait()
	if w := fetch(); w.Code != 200 {
		t.Fatalf("capacity not recovered: %d", w.Code)
	}
}

func TestReadOnlyMiniAppKeepsOfflineProfiles(t *testing.T) {
	p, _ := New("https://panel.example/api/subscription", "key", "sub")
	p.SetCircuitBreaker(0, 0)
	p.remember("saved", tokenPath, entry{body: []byte("profile"), header: http.Header{"Content-Type": []string{"text/plain"}}}, p.generation())
	p.client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: http.NoBody}, nil
	})}
	for _, path := range []string{"/tg/session", "/tg/shop", "/tg/promo-history"} {
		w := httptest.NewRecorder()
		p.ServeHTTP(w, httptest.NewRequest("POST", "https://sub.example/sub"+path, nil))
		if len(p.profiles) != 1 {
			t.Fatalf("read-only %s erased cached profiles", path)
		}
	}
	w := httptest.NewRecorder()
	p.ServeHTTP(w, httptest.NewRequest("POST", "https://sub.example/sub/tg/reissue", nil))
	if len(p.profiles) != 0 {
		t.Fatal("reissue failed to invalidate profiles")
	}
}

func TestCancelledRequestDoesNotReceiveOfflineProfile(t *testing.T) {
	p, _ := New("https://panel.example/api/subscription", "key", "sub")
	p.SetCircuitBreaker(0, 0)
	p.remember("saved", tokenPath, entry{body: []byte("profile"), header: http.Header{"Content-Type": []string{"text/plain"}}}, p.generation())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequest("GET", "https://sub.example/", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	if p.offline(w, r, "saved") || w.Body.Len() != 0 {
		t.Fatal("served offline response after cancellation")
	}
}
